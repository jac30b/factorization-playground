package main

import (
	"fmt"
	"log"
	"time"

	"github.com/jac30b/factorization-playground/engine"
)

func main() {
	catalog, err := loadCatalog()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("catalog: orders=%d rows, customers=%d rows", len(catalog["orders"].Rows), len(catalog["customers"].Rows))
	queryText := "SELECT o.customer_id, COUNT(*) FROM orders o JOIN customers c ON o.customer_id = c.id GROUP BY o.customer_id"
	log.Printf("query: %s", queryText)

	query, err := engine.Parse(queryText)
	if err != nil {
		log.Fatal(err)
	}
	plan, err := engine.Plan(catalog, query)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("plan:\n%s", plan.Explain())

	started := time.Now()
	rows, err := plan.Execute()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("execution time: %s", time.Since(started))
	fmt.Printf("result groups: %d\n", len(rows))
}

func loadCatalog() (engine.Catalog, error) {
	orders, err := engine.LoadCSV("data/orders.csv")
	if err != nil {
		return nil, fmt.Errorf("load orders: %w", err)
	}
	customers, err := engine.LoadCSV("data/customers.csv")
	if err != nil {
		return nil, fmt.Errorf("load customers: %w", err)
	}
	return engine.Catalog{"orders": orders, "customers": customers}, nil
}
