package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Tables is the CREATE TABLE and CREATE INDEX statements for the tables
// pillar.json declares, as the host makes them.
//
// On the host the deploy makes the tables before the container starts, and
// this is never asked. It is for a database that nothing deployed: the tests'
// temporary files, and cmd/localdb's for running the service on a desk. Both
// read the same pillar.json the deploy sends, so the tables they get are the
// tables the host has, and a column this package reads that the file does not
// declare fails a test rather than a deploy.
func Tables(pillar []byte) ([]string, error) {
	var file struct {
		Workload struct {
			Database struct {
				Tables map[string]struct {
					Columns []struct {
						Name    string          `json:"name"`
						Type    string          `json:"type"`
						NotNull bool            `json:"not_null"`
						Default json.RawMessage `json:"default"`
					} `json:"columns"`
					PrimaryKey []string `json:"primary_key"`
					Indexes    map[string]struct {
						Columns []string `json:"columns"`
						Unique  bool     `json:"unique"`
					} `json:"indexes"`
				} `json:"tables"`
			} `json:"database"`
		} `json:"workload"`
	}
	if err := json.Unmarshal(pillar, &file); err != nil {
		return nil, err
	}

	tables := file.Workload.Database.Tables
	if len(tables) == 0 {
		return nil, fmt.Errorf("pillar.json declares no tables")
	}

	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)

	var statements []string
	for _, name := range names {
		table := tables[name]

		var parts []string
		for _, column := range table.Columns {
			part := fmt.Sprintf("%q %s", column.Name, strings.ToUpper(column.Type))
			if column.NotNull {
				part += " NOT NULL"
			}
			if len(column.Default) > 0 {
				literal, err := defaultLiteral(column.Default)
				if err != nil {
					return nil, fmt.Errorf("%s.%s: %w", name, column.Name, err)
				}
				part += " DEFAULT " + literal
			}
			parts = append(parts, part)
		}

		if len(table.PrimaryKey) > 0 {
			quoted := make([]string, len(table.PrimaryKey))
			for i, column := range table.PrimaryKey {
				quoted[i] = fmt.Sprintf("%q", column)
			}
			parts = append(parts, "PRIMARY KEY ("+strings.Join(quoted, ", ")+")")
		}

		statements = append(statements, fmt.Sprintf("CREATE TABLE IF NOT EXISTS %q (%s) STRICT", name, strings.Join(parts, ", ")))

		indexes := make([]string, 0, len(table.Indexes))
		for index := range table.Indexes {
			indexes = append(indexes, index)
		}
		sort.Strings(indexes)

		for _, index := range indexes {
			quoted := make([]string, len(table.Indexes[index].Columns))
			for i, column := range table.Indexes[index].Columns {
				quoted[i] = fmt.Sprintf("%q", column)
			}

			unique := ""
			if table.Indexes[index].Unique {
				unique = "UNIQUE "
			}

			statements = append(statements, fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %q ON %q (%s)", unique, index, name, strings.Join(quoted, ", ")))
		}
	}

	return statements, nil
}

// defaultLiteral is a column's json default as SQL: a string quoted, a number
// as it is.
func defaultLiteral(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return "'" + strings.ReplaceAll(text, "'", "''") + "'", nil
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}

	return "", fmt.Errorf("a default has to be a string or a number, not %s", raw)
}
