// Command generate-data creates deterministic CSV fixtures for the playground.
package main

import (
	"encoding/csv"
	"log"
	"os"
	"strconv"
)

func main() {
	file, err := os.Create("data/orders.csv")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if err := writer.Write([]string{"customer_id"}); err != nil {
		log.Fatal(err)
	}
	for row := 0; row < 1_000; row++ {
		customerID := row%3 + 1
		if err := writer.Write([]string{strconv.Itoa(customerID)}); err != nil {
			log.Fatal(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		log.Fatal(err)
	}
}
