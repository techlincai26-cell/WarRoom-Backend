package db

import (
	"database/sql"
	"database/sql/driver"
)

// Helper to open DB from connector since sql.OpenDB is available in Go 1.10+
func openDBWithConnector(connector driver.Connector) *sql.DB {
	return sql.OpenDB(connector)
}
