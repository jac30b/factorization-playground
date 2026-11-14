# Factorization playground

A deliberately small Go execution engine for experimenting with factorized
query processing. It is not a database: CSV files supply data, and the SQL
surface is purposefully limited to a join graph, `COUNT(*)`, and `GROUP BY`.

```sql
SELECT o.customer_id, COUNT(*)
FROM orders o
JOIN customers c ON o.customer_id = c.id
GROUP BY o.customer_id
```

SQL text is parsed by Vitess's maintained MySQL parser. The engine then
translates only its supported AST subset: an unfiltered left-deep tree of
equi-`JOIN`s with `SELECT [grouped columns,] COUNT(*)` and `GROUP BY`. Keeping
the grammar external avoids making parser work part of a join-execution
experiment.

The current path is `Scan -> HashJoin -> HashAggregate`. `HashJoin` materializes
a flat row for every matching pair. That is the baseline which a future
`LinearChainedJoin` or `FactorizedJoin` will replace. Both use the same
`Operator` interface, planner, and test fixtures.

The build-side hash table is isolated in `engine/ht.go`, so experiments can
replace only the key-to-row data structure while retaining the baseline join
operator. Its `Key` interface keeps hash calculation separate from equality
testing; `IntKey` is the current integer-only implementation. `HashTable` uses
standard-library FNV-1a to hash keys, maps the result to an explicit bucket by
modulo, then linearly probes for a distinct key. Each bucket is a single 64-bit
word: a 16-bit Bloom filter salt and a 48-bit arena index pointing to a linked
list of duplicate-key rows. The salt uses `bits-and-blooms/bloom/v3` with two
hash locations over the key's hash. Probing rejects buckets missing any query
salt bits; possible matches still require full hash and key equality checks.

Run the example:

```powershell
go run .
```

Run tests:

```powershell
go test ./...
```

Current non-goals: persistence, transactions, a general SQL grammar, query
optimization, and non-integer data. Keeping those out is intentional: the
experiment should concentrate on the join representation and aggregation.

## Data

`main.go` loads `data/orders.csv` and `data/customers.csv`. Each CSV requires a
header row and currently supports signed 64-bit integer fields only. Replace
these files with larger relations to run the same query at a different scale.
The bundled `orders.csv` contains 1,000 rows. Regenerate it with:

```powershell
go run ./cmd/generate-data
```
