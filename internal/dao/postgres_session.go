package dao

import (
	"database/sql"
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// OpenIndependentPostgresDB opens a separate single-connection pool using the
// active PostgreSQL or Vastbase dialector settings so session advisory locks
// never consume connections from the shared application pool.
func OpenIndependentPostgresDB(db *gorm.DB) (*sql.DB, error) {
	if db == nil {
		return nil, fmt.Errorf("independent session requires a PostgreSQL dialector")
	}
	var config *postgres.Config
	switch dialector := db.Dialector.(type) {
	case postgresMetaDialector:
		if dialector.Dialector != nil {
			config = dialector.Config
		}
	case vastbaseDialector:
		if dialector.Dialector != nil {
			config = dialector.Config
		}
	case *postgres.Dialector:
		config = dialector.Config
	default:
		return nil, fmt.Errorf("independent session requires a PostgreSQL dialector")
	}
	if config == nil || config.DSN == "" {
		return nil, fmt.Errorf("independent session requires a PostgreSQL DSN")
	}
	driverName := config.DriverName
	if driverName == "" {
		driverName = "pgx"
	}
	pool, err := sql.Open(driverName, config.DSN)
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(0)
	return pool, nil
}
