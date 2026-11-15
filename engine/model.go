package engine

// Row uses qualified column names (for example, "orders.id") after scanning.
// Integers are enough for the join and cardinality experiments in this project.
type Row map[string]int64

// Table is an in-memory input relation. Columns must be unqualified here.
type Table struct {
	Columns []string
	Rows    [][]int64
}

// Catalog holds the base relations available to a query.
type Catalog map[string]Table

// Query is the parsed, intentionally narrow SQL representation.
type Query struct {
	From    Source
	Joins   []Join
	GroupBy []string // qualified column names
}

type Source struct {
	Table string
	Alias string
}

type Join struct {
	Source Source
	Left   string // qualified column name
	Right  string // qualified column name
}

// Operator exposes flat results at the query boundary. Factorized operators
// also implement an internal interface used by joins and aggregation to retain
// grouped row references until the final result is produced.
type Operator interface {
	Execute() ([]Row, error)
	Explain() string
}
