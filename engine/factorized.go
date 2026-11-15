package engine

import (
	"fmt"
	"strings"
)

// A rowFactor is either a shared HT duplicate chain or a subset of source row
// references. All rows belong to one source alias. No joined row maps are stored.
// Hash tables are fully built before factors referring to them are published.
type rowFactor struct {
	table     *HashTable
	groupID   uint64
	keyColumn string
	rows      []Row
}

func (f rowFactor) count() int64 {
	if f.table != nil {
		return int64(f.table.group(f.groupID).count)
	}
	return int64(len(f.rows))
}

func (f rowFactor) first() Row {
	if f.table != nil {
		return f.table.node(f.table.group(f.groupID).head).row
	}
	return f.rows[0]
}

func (f rowFactor) each(visit func(Row) error) error {
	if f.table != nil {
		for id := f.table.group(f.groupID).head; id != 0; id = f.table.node(id).next {
			if err := visit(f.table.node(id).row); err != nil {
				return err
			}
		}
		return nil
	}
	for _, row := range f.rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

// A product represents the Cartesian product of independent factors. A relation
// is a union of these products, preserving duplicates (bag semantics).
type factorProduct []rowFactor

type factorizedOperator interface {
	executeFactors() ([]factorProduct, error)
}

func executeFactors(input Operator) ([]factorProduct, error) {
	if op, ok := input.(factorizedOperator); ok {
		return op.executeFactors()
	}
	rows, err := input.Execute()
	if err != nil {
		return nil, err
	}
	products := make([]factorProduct, 0, len(rows))
	for _, row := range rows {
		products = append(products, factorProduct{{rows: []Row{row}}})
	}
	return products, nil
}

// FactorizedJoin retains references to matching HT groups rather than expanding
// their rows. The planner uses scans on the right and factorized joins on the left.
type FactorizedJoin struct {
	Left, Right       Operator
	LeftKey, RightKey string
}

func (j FactorizedJoin) executeFactors() ([]factorProduct, error) {
	buildRows, err := j.Right.Execute()
	if err != nil {
		return nil, err
	}
	table := NewHashTable(len(buildRows))
	for _, row := range buildRows {
		key, err := intKeyFromRow(row, j.RightKey, "build")
		if err != nil {
			return nil, err
		}
		table.Insert(key, row)
	}
	products, err := executeFactors(j.Left)
	if err != nil {
		return nil, err
	}
	type partitionKey struct {
		table   *HashTable
		groupID uint64
	}
	partitions := make(map[partitionKey]map[int64]rowFactor)
	var result []factorProduct
	for _, product := range products {
		owner := -1
		for index, factor := range product {
			if _, ok := factor.first()[j.LeftKey]; ok {
				owner = index
				break
			}
		}
		if owner == -1 {
			return nil, fmt.Errorf("hash join probe key %q does not exist", j.LeftKey)
		}
		factor := product[owner]
		cacheKey := partitionKey{factor.table, factor.groupID}
		parts := partitions[cacheKey]
		if factor.table == nil || parts == nil {
			parts, err = partitionFactor(factor, j.LeftKey)
			if err != nil {
				return nil, err
			}
			if factor.table != nil {
				partitions[cacheKey] = parts
			}
		}
		for key, part := range parts {
			id := table.lookupGroup(IntKey(key))
			if id == 0 {
				continue
			}
			joined := make(factorProduct, len(product)+1)
			copy(joined, product)
			joined[owner] = part
			joined[len(product)] = rowFactor{table: table, groupID: id, keyColumn: j.RightKey}
			result = append(result, joined)
		}
	}
	return result, nil
}

// Refining only the factor containing the next join key preserves correlations
// between columns of that source, without expanding unrelated factors.
func partitionFactor(f rowFactor, column string) (map[int64]rowFactor, error) {
	if f.keyColumn == column || f.count() == 1 {
		value, ok := f.first()[column]
		if !ok {
			return nil, fmt.Errorf("hash join probe key %q does not exist", column)
		}
		return map[int64]rowFactor{value: f}, nil
	}
	parts := make(map[int64]rowFactor)
	err := f.each(func(row Row) error {
		value, ok := row[column]
		if !ok {
			return fmt.Errorf("hash join probe key %q does not exist", column)
		}
		part := parts[value]
		part.rows = append(part.rows, row)
		parts[value] = part
		return nil
	})
	return parts, err
}

// Execute explicitly expands results for callers requiring the flat Operator
// interface. Planned aggregation uses executeFactors and never calls this path.
func (j FactorizedJoin) Execute() ([]Row, error) {
	products, err := j.executeFactors()
	if err != nil {
		return nil, err
	}
	var rows []Row
	for _, product := range products {
		var expand func(int, Row) error
		expand = func(index int, row Row) error {
			if index == len(product) {
				rows = append(rows, row)
				return nil
			}
			return product[index].each(func(part Row) error {
				return expand(index+1, merge(row, part))
			})
		}
		if err := expand(0, nil); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (j FactorizedJoin) Explain() string {
	return "FactorizedJoin(" + j.LeftKey + " = " + j.RightKey + ")\n  " + strings.ReplaceAll(j.Left.Explain(), "\n", "\n  ") + "\n  " + strings.ReplaceAll(j.Right.Explain(), "\n", "\n  ")
}

type countedValues struct {
	values []int64
	count  int64
}

// Group each factor locally, then combine distinct projected values. Columns
// absent from GROUP BY contribute only their factor's multiplicity.
func aggregateFactors(input Operator, columns []string) ([]Row, error) {
	products, err := executeFactors(input)
	if err != nil {
		return nil, err
	}
	groups := make(map[string]countedValues)
	// Shared HT groups can appear in many products. Summarize their projected
	// values once rather than traversing the same duplicate chain per probe row.
	type projectionKey struct {
		table   *HashTable
		groupID uint64
		columns string
	}
	projections := make(map[projectionKey][]countedValues)
	for _, product := range products {
		owners := make([][]int, len(product))
		for index, column := range columns {
			found := false
			for owner, factor := range product {
				if _, ok := factor.first()[column]; ok {
					owners[owner] = append(owners[owner], index)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("GROUP BY column %q does not exist", column)
			}
		}
		locals := make([][]countedValues, len(product))
		for owner, factor := range product {
			indexes := owners[owner]
			if len(indexes) == 0 {
				locals[owner] = []countedValues{{count: factor.count()}}
				continue
			}
			projectedColumns := make([]string, len(indexes))
			for i, index := range indexes {
				projectedColumns[i] = columns[index]
			}
			cacheKey := projectionKey{factor.table, factor.groupID, strings.Join(projectedColumns, "\x00")}
			if cached, ok := projections[cacheKey]; factor.table != nil && ok {
				locals[owner] = cached
				continue
			}
			local := make(map[string]countedValues)
			visit := func(row Row) error {
				values := make([]int64, len(indexes))
				for i, index := range indexes {
					value, ok := row[columns[index]]
					if !ok {
						return fmt.Errorf("GROUP BY column %q does not exist", columns[index])
					}
					values[i] = value
				}
				key := groupKey(values)
				entry := local[key]
				entry.values = values
				entry.count++
				local[key] = entry
				return nil
			}
			if err := factor.each(visit); err != nil {
				return nil, err
			}
			for _, entry := range local {
				locals[owner] = append(locals[owner], entry)
			}
			if factor.table != nil {
				projections[cacheKey] = locals[owner]
			}
		}
		values := make([]int64, len(columns))
		var combine func(int, int64)
		combine = func(owner int, count int64) {
			if owner == len(product) {
				key := groupKey(values)
				entry := groups[key]
				if entry.values == nil {
					entry.values = append([]int64(nil), values...)
				}
				entry.count += count
				groups[key] = entry
				return
			}
			for _, entry := range locals[owner] {
				for i, index := range owners[owner] {
					values[index] = entry.values[i]
				}
				combine(owner+1, count*entry.count)
			}
		}
		combine(0, 1)
	}
	if len(columns) == 0 && len(groups) == 0 {
		groups[""] = countedValues{}
	}
	result := make([]Row, 0, len(groups))
	for _, group := range groups {
		row := Row{"count": group.count}
		for index, column := range columns {
			row[column] = group.values[index]
		}
		result = append(result, row)
	}
	return result, nil
}
