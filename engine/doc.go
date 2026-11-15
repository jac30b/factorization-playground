// Package engine is a deliberately small relational execution playground.
//
// It supports just enough SQL to study join implementations:
//
//	SELECT COUNT(*) FROM orders o JOIN lineitems l ON o.id = l.order_id
//
// It is intentionally not a database: there is no storage manager, type
// coercion, transaction handling, or optimizer. A parsed query becomes a
// left-deep physical plan of Scan -> FactorizedJoin -> HashAggregate operators. Joins retain shared
// duplicate-row groups; COUNT and GROUP BY consume their multiplicities without
// materializing every joined row pair. HashJoin remains as a flat baseline.
package engine
