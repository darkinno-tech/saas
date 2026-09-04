package migration

import (
	"fmt"
	"strings"
)

// Column describes a column in a created table.
type Column struct {
	Name    string
	Type    string
	NotNull bool
	// Default is a raw SQL default expression; empty means no default.
	Default string
}

// CreateTable returns SQL to create a table from the given columns and unique keys.
func (planner Planner) CreateTable(table string, columns []Column, uniqueKeys [][]string) (string, error) {
	if err := planner.validateDialect(); err != nil {
		return "", err
	}
	if len(columns) == 0 {
		return "", ErrInvalidMigration
	}
	if !isSafeQualifiedIdentifier(table) {
		return "", ErrInvalidIdentifier
	}

	parts := make([]string, 0, len(columns)+len(uniqueKeys))
	for _, column := range columns {
		if !isSafeIdentifier(column.Name) {
			return "", ErrInvalidIdentifier
		}
		if column.Type == "" || strings.ContainsAny(column.Type, ";") {
			return "", ErrInvalidMigration
		}
		def := column.Name + " " + column.Type
		if column.NotNull {
			def += " NOT NULL"
		}
		if column.Default != "" {
			if strings.ContainsAny(column.Default, ";") {
				return "", ErrInvalidMigration
			}
			def += " DEFAULT " + column.Default
		}
		parts = append(parts, def)
	}
	for _, key := range uniqueKeys {
		if len(key) == 0 {
			return "", ErrInvalidMigration
		}
		columns := make([]string, len(key))
		for i, name := range key {
			if !isSafeIdentifier(name) {
				return "", ErrInvalidIdentifier
			}
			columns[i] = name
		}
		parts = append(parts, "UNIQUE ("+strings.Join(columns, ", ")+")")
	}
	return fmt.Sprintf("CREATE TABLE %s (%s)", table, strings.Join(parts, ", ")), nil
}
