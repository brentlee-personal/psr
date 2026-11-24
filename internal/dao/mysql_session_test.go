package dao

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestIndependentMySQLDBSupportsMigrationDialector(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		name := "mysql"
		if wrapped {
			name = "migration wrapper"
		}
		t.Run(name, func(t *testing.T) {
			shared, _, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { shared.Close() })
			base := mysql.New(mysql.Config{DSN: "test:test@tcp(localhost:3306)/test?parseTime=True", Conn: shared, SkipInitializeWithVersion: true}).(*mysql.Dialector)
			var dialect gorm.Dialector = base
			if wrapped {
				dialect = migrationAwareDialector{Dialector: base}
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			independent, err := OpenIndependentMySQLDB(db)
			if err != nil {
				t.Fatal(err)
			}
			defer independent.Close()
			if independent == shared || independent.Stats().MaxOpenConnections != 1 {
				t.Fatal("session lock must have its own bounded connection pool")
			}
		})
	}
}

func TestOpenIndependentPostgresDB(t *testing.T) {
	for _, variant := range []string{"postgres", "postgresMetaDialector", "vastbaseDialector"} {
		t.Run(variant, func(t *testing.T) {
			shared, _, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { shared.Close() })
			base := postgres.New(postgres.Config{
				DriverName: "pgx",
				DSN:        "host=localhost port=5432 user=test dbname=test sslmode=disable",
				Conn:       shared,
			}).(*postgres.Dialector)
			var dialect gorm.Dialector = base
			switch variant {
			case "postgresMetaDialector":
				dialect = postgresMetaDialector{Dialector: base}
			case "vastbaseDialector":
				dialect = vastbaseDialector{Dialector: base}
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			independent, err := OpenIndependentPostgresDB(db)
			if err != nil {
				t.Fatal(err)
			}
			defer independent.Close()
			if independent == shared || independent.Stats().MaxOpenConnections != 1 {
				t.Fatal("postgres session lock must have its own bounded connection pool")
			}
		})
	}

	t.Run("rejects-mysql-dialector", func(t *testing.T) {
		shared, _, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { shared.Close() })
		db, err := gorm.Open(mysql.New(mysql.Config{
			DSN:                       "test:test@tcp(localhost:3306)/test?parseTime=True",
			Conn:                      shared,
			SkipInitializeWithVersion: true,
		}), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenIndependentPostgresDB(db); err == nil {
			t.Fatal("expected OpenIndependentPostgresDB to reject non-Postgres dialector")
		}
	})
}
