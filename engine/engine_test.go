package engine

import "testing"

func TestQueryCountsFlatJoinResults(t *testing.T) {
	catalog := Catalog{
		"orders":    {Columns: []string{"customer"}, Rows: [][]int64{{1}, {1}, {2}}},
		"customers": {Columns: []string{"id"}, Rows: [][]int64{{1}, {1}, {2}}},
	}
	query, err := Parse("SELECT COUNT(*) FROM orders o JOIN customers c ON c.id = o.customer")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(catalog, query)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := plan.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rows[0]["count"], int64(5); got != want {
		t.Fatalf("count = %d, want %d", got, want)
	}
}

func TestRejectsDisconnectedJoin(t *testing.T) {
	query, err := Parse("SELECT COUNT(*) FROM a JOIN b ON a.id = c.id")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Plan(Catalog{"a": {}, "b": {}}, query)
	if err == nil {
		t.Fatal("expected planning error")
	}
}

func TestQueryGroupsFlatJoinResults(t *testing.T) {
	catalog := Catalog{
		"orders":    {Columns: []string{"customer"}, Rows: [][]int64{{1}, {1}, {2}}},
		"customers": {Columns: []string{"id"}, Rows: [][]int64{{1}, {1}, {2}}},
	}
	query, err := Parse("SELECT o.customer, COUNT(*) FROM orders o JOIN customers c ON c.id = o.customer GROUP BY o.customer")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(catalog, query)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := plan.Execute()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int64]int64{}
	for _, row := range rows {
		counts[row["o.customer"]] = row["count"]
	}
	if got, want := counts[1], int64(4); got != want {
		t.Fatalf("customer 1 count = %d, want %d", got, want)
	}
	if got, want := counts[2], int64(1); got != want {
		t.Fatalf("customer 2 count = %d, want %d", got, want)
	}
}
