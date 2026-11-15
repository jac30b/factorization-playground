package engine

import (
	"reflect"
	"strings"
	"testing"
)

// Compare against independently counted flat join rows, including joins on a
// non-key column of an earlier duplicate group (which must split that factor).
func TestFactorizedQueriesMatchFlatJoins(t *testing.T) {
	catalog := Catalog{
		"a":      {Columns: []string{"id", "label"}, Rows: [][]int64{{1, 7}, {1, 8}, {2, 7}, {9, 0}}},
		"b":      {Columns: []string{"id", "next", "label"}, Rows: [][]int64{{1, 10, 3}, {1, 20, 4}, {1, 10, 3}, {2, 30, 5}}},
		"c":      {Columns: []string{"id", "label"}, Rows: [][]int64{{10, 6}, {10, 6}, {20, 7}}},
		"d":      {Columns: []string{"id", "label"}, Rows: [][]int64{{1, 9}, {1, 9}, {2, 8}, {3, 7}, {4, 6}}},
		"vacant": {Columns: []string{"id"}},
	}
	for _, sql := range []string{
		"SELECT COUNT(*) FROM a",
		"SELECT COUNT(*) FROM a JOIN b ON a.id = b.id",
		"SELECT a.label, COUNT(*) FROM a JOIN b ON a.id = b.id GROUP BY a.label",
		"SELECT b.label, COUNT(*) FROM a JOIN b ON a.id = b.id GROUP BY b.label",
		"SELECT a.label, b.label, COUNT(*) FROM a JOIN b ON a.id = b.id GROUP BY a.label, b.label",
		"SELECT COUNT(*) FROM a JOIN b ON a.id = b.id JOIN c ON b.next = c.id",
		"SELECT b.label, c.label, COUNT(*) FROM a JOIN b ON a.id = b.id JOIN c ON b.next = c.id GROUP BY b.label, c.label",
		"SELECT a.label, c.label, COUNT(*) FROM a JOIN b ON a.id = b.id JOIN c ON a.id = c.id GROUP BY a.label, c.label",
		"SELECT a.label, d.label, COUNT(*) FROM a JOIN b ON a.id = b.id JOIN d ON a.id = d.id GROUP BY a.label, d.label",
		"SELECT b.label, c.label, d.label, COUNT(*) FROM a JOIN b ON a.id = b.id JOIN c ON b.next = c.id JOIN d ON b.label = d.id GROUP BY b.label, c.label, d.label",
		"SELECT COUNT(*) FROM a JOIN vacant e ON a.id = e.id",
		"SELECT a.id, COUNT(*) FROM a JOIN vacant e ON a.id = e.id GROUP BY a.id",
		"SELECT COUNT(*) FROM vacant e JOIN b ON e.id = b.id",
	} {
		t.Run(sql, func(t *testing.T) {
			query, err := Parse(sql)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Plan(catalog, query)
			if err != nil {
				t.Fatal(err)
			}
			agg := plan.(aggregate)
			flat, err := flattenPlan(agg.input).Execute()
			if err != nil {
				t.Fatal(err)
			}
			want := make(map[string]int64)
			for _, row := range flat {
				values := make([]int64, len(query.GroupBy))
				for i, column := range query.GroupBy {
					values[i] = row[column]
				}
				want[groupKey(values)]++
			}
			if len(query.GroupBy) == 0 && len(want) == 0 {
				want[""] = 0
			}
			rows, err := plan.Execute()
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[string]int64)
			for _, row := range rows {
				values := make([]int64, len(query.GroupBy))
				for i, column := range query.GroupBy {
					values[i] = row[column]
				}
				got[groupKey(values)] = row["count"]
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func flattenPlan(op Operator) Operator {
	if j, ok := op.(FactorizedJoin); ok {
		return HashJoin{Left: flattenPlan(j.Left), Right: flattenPlan(j.Right), LeftKey: j.LeftKey, RightKey: j.RightKey}
	}
	return op
}

// Aggregation must choose the factor interface even when flat execution exists.
type factorsOnly struct{ products []factorProduct }

func (f factorsOnly) Execute() ([]Row, error)                  { panic("unexpected flat materialization") }
func (f factorsOnly) Explain() string                          { return "FactorsOnly" }
func (f factorsOnly) executeFactors() ([]factorProduct, error) { return f.products, nil }

func TestFactorizedJoinRetainsSharedGroups(t *testing.T) {
	const size = 2000
	table := Table{Columns: []string{"id"}, Rows: make([][]int64, size)}
	for i := range table.Rows {
		table.Rows[i] = []int64{1}
	}
	j := FactorizedJoin{
		Left:    scan{source: Source{Alias: "a"}, table: table},
		Right:   scan{source: Source{Alias: "b"}, table: table},
		LeftKey: "a.id", RightKey: "b.id",
	}
	products, err := j.executeFactors()
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != size {
		t.Fatalf("products = %d, want %d", len(products), size)
	}
	shared := products[0][1]
	for _, product := range products {
		if len(product) != 2 || product[1].count() != size || product[1].table != shared.table || product[1].groupID != shared.groupID {
			t.Fatal("join did not retain shared duplicate groups")
		}
	}
	for _, columns := range [][]string{nil, {"a.id"}, {"b.id"}, {"a.id", "b.id"}} {
		rows, err := (aggregate{input: factorsOnly{products}, groupBy: columns}).Execute()
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0]["count"] != size*size {
			t.Fatalf("got %v, want count %d", rows, size*size)
		}
	}
}

func TestFactorizedMissingColumns(t *testing.T) {
	catalog := Catalog{
		"a": {Columns: []string{"id"}, Rows: [][]int64{{1}}},
		"b": {Columns: []string{"id"}, Rows: [][]int64{{1}}},
	}
	for _, sql := range []string{
		"SELECT COUNT(*) FROM a JOIN b ON a.missing = b.id",
		"SELECT COUNT(*) FROM a JOIN b ON a.id = b.missing",
		"SELECT a.missing, COUNT(*) FROM a JOIN b ON a.id = b.id GROUP BY a.missing",
	} {
		query, err := Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := Plan(catalog, query)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plan.Execute(); err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("query %s: expected missing column error, got %v", sql, err)
		}
	}
}

func TestFactorizedJoinFlatCompatibility(t *testing.T) {
	table := Table{Columns: []string{"id"}, Rows: [][]int64{{1}, {1}, {2}}}
	j := FactorizedJoin{
		Left:    scan{source: Source{Alias: "a"}, table: table},
		Right:   scan{source: Source{Alias: "b"}, table: table},
		LeftKey: "a.id", RightKey: "b.id",
	}
	rows, err := j.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d, want 5", len(rows))
	}
	for _, row := range rows {
		if row["a.id"] != row["b.id"] || len(row) != 2 {
			t.Fatalf("invalid row: %v", row)
		}
	}
}
