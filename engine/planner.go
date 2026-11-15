package engine

import (
	"fmt"
	"strings"
)

// Plan turns a parsed query into the factorized physical plan.
func Plan(catalog Catalog, query Query) (Operator, error) {
	root, err := makeScan(catalog, query.From)
	if err != nil {
		return nil, err
	}
	knownAliases := map[string]bool{query.From.Alias: true}
	for _, join := range query.Joins {
		right, err := makeScan(catalog, join.Source)
		if err != nil {
			return nil, err
		}
		if knownAliases[join.Source.Alias] {
			return nil, fmt.Errorf("duplicate table alias %q", join.Source.Alias)
		}
		leftKey, rightKey, err := orientJoin(join, knownAliases)
		if err != nil {
			return nil, err
		}
		root = FactorizedJoin{Left: root, Right: right, LeftKey: leftKey, RightKey: rightKey}
		knownAliases[join.Source.Alias] = true
	}
	return aggregate{input: root, groupBy: query.GroupBy}, nil
}

func makeScan(catalog Catalog, source Source) (Operator, error) {
	table, ok := catalog[source.Table]
	if !ok {
		return nil, fmt.Errorf("unknown table %q", source.Table)
	}
	return scan{source: source, table: table}, nil
}

func orientJoin(join Join, knownAliases map[string]bool) (string, string, error) {
	leftAlias := strings.Split(join.Left, ".")[0]
	rightAlias := strings.Split(join.Right, ".")[0]
	newAlias := join.Source.Alias
	switch {
	case knownAliases[leftAlias] && rightAlias == newAlias:
		return join.Left, join.Right, nil
	case knownAliases[rightAlias] && leftAlias == newAlias:
		return join.Right, join.Left, nil
	default:
		return "", "", fmt.Errorf("JOIN %q must connect the new alias %q to an earlier source", join.Left+" = "+join.Right, newAlias)
	}
}
