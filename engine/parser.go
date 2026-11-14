package engine

import (
	"fmt"

	"vitess.io/vitess/go/vt/sqlparser"
)

// Parse uses Vitess's production SQL parser, then rejects AST shapes the small
// executor cannot run. The accepted execution subset is deliberately narrow:
// SELECT [grouped columns,] COUNT(*) FROM <table> [alias] (JOIN <table> [alias]
// ON <alias.column> = <alias.column>)* [GROUP BY grouped columns].
func Parse(sql string) (Query, error) {
	statement, err := sqlparser.NewTestParser().Parse(sql)
	if err != nil {
		return Query{}, err
	}
	selectStatement, ok := statement.(*sqlparser.Select)
	if !ok {
		return Query{}, fmt.Errorf("only SELECT statements are supported")
	}
	if selectStatement.Where != nil || selectStatement.Having != nil || selectStatement.Limit != nil || len(selectStatement.From) != 1 {
		return Query{}, fmt.Errorf("only an unfiltered single-FROM join tree is supported")
	}
	selectedGroups, err := selectedGroupColumns(selectStatement.SelectExprs)
	if err != nil {
		return Query{}, err
	}
	groupBy, err := groupByColumns(selectStatement.GroupBy)
	if err != nil {
		return Query{}, err
	}
	if !sameColumns(selectedGroups, groupBy) {
		return Query{}, fmt.Errorf("selected columns and GROUP BY columns must match")
	}
	from, joins, err := flattenTableExpr(selectStatement.From[0])
	if err != nil {
		return Query{}, err
	}
	return Query{From: from, Joins: joins, GroupBy: groupBy}, nil
}

func selectedGroupColumns(expressions *sqlparser.SelectExprs) ([]string, error) {
	if expressions == nil || len(expressions.Exprs) == 0 {
		return nil, fmt.Errorf("SELECT must include COUNT(*)")
	}
	groups := make([]string, 0, len(expressions.Exprs)-1)
	countStar := false
	for _, expression := range expressions.Exprs {
		aliased, ok := expression.(*sqlparser.AliasedExpr)
		if !ok {
			return nil, fmt.Errorf("SELECT supports only qualified columns and COUNT(*)")
		}
		switch value := aliased.Expr.(type) {
		case *sqlparser.CountStar:
			if countStar {
				return nil, fmt.Errorf("SELECT must contain exactly one COUNT(*)")
			}
			countStar = true
		case *sqlparser.ColName:
			column, ok := qualifiedColumn(value)
			if !ok {
				return nil, fmt.Errorf("selected columns must be qualified")
			}
			groups = append(groups, column)
		default:
			return nil, fmt.Errorf("SELECT supports only qualified columns and COUNT(*)")
		}
	}
	if !countStar {
		return nil, fmt.Errorf("SELECT must include COUNT(*)")
	}
	return groups, nil
}

func groupByColumns(groupBy *sqlparser.GroupBy) ([]string, error) {
	if groupBy == nil {
		return nil, nil
	}
	if groupBy.WithRollup {
		return nil, fmt.Errorf("GROUP BY WITH ROLLUP is not supported")
	}
	columns := make([]string, 0, len(groupBy.Exprs))
	for _, expression := range groupBy.Exprs {
		column, ok := qualifiedColumn(expression)
		if !ok {
			return nil, fmt.Errorf("GROUP BY columns must be qualified")
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func sameColumns(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, column := range left {
		seen[column] = true
	}
	for _, column := range right {
		if !seen[column] {
			return false
		}
	}
	return true
}

func flattenTableExpr(tableExpr sqlparser.TableExpr) (Source, []Join, error) {
	switch expression := tableExpr.(type) {
	case *sqlparser.AliasedTableExpr:
		table, ok := expression.Expr.(sqlparser.TableName)
		if !ok || table.Qualifier.String() != "" {
			return Source{}, nil, fmt.Errorf("FROM sources must be unqualified table names")
		}
		alias := expression.As.String()
		if alias == "" {
			alias = table.Name.String()
		}
		return Source{Table: table.Name.String(), Alias: alias}, nil, nil
	case *sqlparser.JoinTableExpr:
		if expression.Join != sqlparser.NormalJoinType {
			return Source{}, nil, fmt.Errorf("only INNER JOIN is supported")
		}
		from, joins, err := flattenTableExpr(expression.LeftExpr)
		if err != nil {
			return Source{}, nil, err
		}
		right, rightJoins, err := flattenTableExpr(expression.RightExpr)
		if err != nil || len(rightJoins) != 0 {
			return Source{}, nil, fmt.Errorf("only left-deep join trees are supported")
		}
		if expression.Condition == nil || expression.Condition.On == nil || len(expression.Condition.Using) != 0 {
			return Source{}, nil, fmt.Errorf("JOIN requires a single ON equality")
		}
		comparison, ok := expression.Condition.On.(*sqlparser.ComparisonExpr)
		if !ok || comparison.Operator != sqlparser.EqualOp {
			return Source{}, nil, fmt.Errorf("JOIN requires a single ON equality")
		}
		left, leftOK := qualifiedColumn(comparison.Left)
		rightColumn, rightOK := qualifiedColumn(comparison.Right)
		if !leftOK || !rightOK {
			return Source{}, nil, fmt.Errorf("join columns must be qualified")
		}
		return from, append(joins, Join{Source: right, Left: left, Right: rightColumn}), nil
	default:
		return Source{}, nil, fmt.Errorf("unsupported FROM expression %T", tableExpr)
	}
}

func qualifiedColumn(expression sqlparser.Expr) (string, bool) {
	column, ok := expression.(*sqlparser.ColName)
	if !ok || column.Qualifier.Name.String() == "" {
		return "", false
	}
	return column.Qualifier.Name.String() + "." + column.Name.String(), true
}
