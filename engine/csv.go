package engine

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
)

// LoadCSV reads a header row followed by integer-valued records into a Table.
// The header defines the table's unqualified column names.
func LoadCSV(path string) (Table, error) {
	file, err := os.Open(path)
	if err != nil {
		return Table{}, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return Table{}, fmt.Errorf("read CSV header: %w", err)
	}
	if len(header) == 0 {
		return Table{}, fmt.Errorf("CSV header is empty")
	}

	table := Table{Columns: header}
	for line := 2; ; line++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Table{}, fmt.Errorf("read CSV row %d: %w", line, err)
		}
		if len(record) != len(header) {
			return Table{}, fmt.Errorf("CSV row %d has %d values for %d columns", line, len(record), len(header))
		}
		row := make([]int64, len(record))
		for index, field := range record {
			value, err := strconv.ParseInt(field, 10, 64)
			if err != nil {
				return Table{}, fmt.Errorf("CSV row %d column %q: %w", line, header[index], err)
			}
			row[index] = value
		}
		table.Rows = append(table.Rows, row)
	}
	return table, nil
}
