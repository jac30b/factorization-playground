package engine

import (
	"fmt"
	"strconv"
	"strings"
)

type scan struct {
	source Source
	table  Table
}

func (s scan) Execute() ([]Row, error) {
	result := make([]Row, 0, len(s.table.Rows))
	for rowIndex, values := range s.table.Rows {
		if len(values) != len(s.table.Columns) {
			return nil, fmt.Errorf("table %q row %d has %d values for %d columns", s.source.Table, rowIndex, len(values), len(s.table.Columns))
		}
		row := make(Row, len(values))
		for columnIndex, value := range values {
			row[s.source.Alias+"."+s.table.Columns[columnIndex]] = value
		}
		result = append(result, row)
	}
	return result, nil
}

func (s scan) Explain() string { return "Scan(" + s.source.Table + " AS " + s.source.Alias + ")" }

// HashJoin is the baseline flat implementation. It materializes every matched
// pair, which is exactly the behavior a factorized operator will avoid.
type HashJoin struct {
	Left, Right       Operator
	LeftKey, RightKey string
}

func (j HashJoin) Execute() ([]Row, error) {
	buildRows, err := j.Right.Execute()
	if err != nil {
		return nil, err
	}
	hashTable := NewHashTable(len(buildRows))
	for _, row := range buildRows {
		key, err := intKeyFromRow(row, j.RightKey, "build")
		if err != nil {
			return nil, err
		}
		hashTable.Insert(key, row)
	}

	probeRows, err := j.Left.Execute()
	if err != nil {
		return nil, err
	}
	result := make([]Row, 0)
	for _, probe := range probeRows {
		key, err := intKeyFromRow(probe, j.LeftKey, "probe")
		if err != nil {
			return nil, err
		}
		for _, match := range hashTable.Lookup(key) {
			result = append(result, merge(probe, match))
		}
	}
	return result, nil
}

func intKeyFromRow(row Row, column, side string) (Key, error) {
	value, ok := row[column]
	if !ok {
		return nil, fmt.Errorf("hash join %s key %q does not exist", side, column)
	}
	return IntKey(value), nil
}

func (j HashJoin) Explain() string {
	return "HashJoin(" + j.LeftKey + " = " + j.RightKey + ")\n  " + strings.ReplaceAll(j.Left.Explain(), "\n", "\n  ") + "\n  " + strings.ReplaceAll(j.Right.Explain(), "\n", "\n  ")
}

// aggregate is the baseline flat GROUP BY implementation. It receives already
// materialized join rows; a factorized aggregation can replace it later.
type aggregate struct {
	input   Operator
	groupBy []string
}

func (a aggregate) Execute() ([]Row, error) {
	rows, err := a.input.Execute()
	if err != nil {
		return nil, err
	}
	type group struct {
		values []int64
		count  int64
	}
	groups := make(map[string]*group)
	for _, row := range rows {
		values := make([]int64, len(a.groupBy))
		for index, column := range a.groupBy {
			value, ok := row[column]
			if !ok {
				return nil, fmt.Errorf("GROUP BY column %q does not exist", column)
			}
			values[index] = value
		}
		key := groupKey(values)
		if groups[key] == nil {
			groups[key] = &group{values: values}
		}
		groups[key].count++
	}
	if len(a.groupBy) == 0 && len(groups) == 0 {
		groups[""] = &group{}
	}
	result := make([]Row, 0, len(groups))
	for _, group := range groups {
		row := Row{"count": group.count}
		for index, column := range a.groupBy {
			row[column] = group.values[index]
		}
		result = append(result, row)
	}
	return result, nil
}

func (a aggregate) Explain() string {
	description := "HashAggregate(COUNT(*))"
	if len(a.groupBy) != 0 {
		description += " GROUP BY " + strings.Join(a.groupBy, ", ")
	}
	return description + "\n  " + strings.ReplaceAll(a.input.Explain(), "\n", "\n  ")
}

func groupKey(values []int64) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = strconv.FormatInt(value, 10)
	}
	return strings.Join(parts, "\x00")
}

func merge(left, right Row) Row {
	joined := make(Row, len(left)+len(right))
	for key, value := range left {
		joined[key] = value
	}
	for key, value := range right {
		joined[key] = value
	}
	return joined
}
