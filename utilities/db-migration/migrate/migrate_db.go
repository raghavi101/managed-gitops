package migrate

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	migrate "github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	db "github.com/redhat-appstudio/managed-gitops/backend-shared/db"
)

func Migrate(opType string, migrationPath string) error {
	addr, password, dbName := db.GetAddrAndPassword()
	port := 5432

	// Base64 strings can contain '/' characters, which mess up URL parsing.
	// So we substitute it with a URL-friendly character.
	password = strings.ReplaceAll(password, "/", "%2f")

	m, err := migrate.New(
		migrationPath,
		fmt.Sprintf("postgresql://postgres:%s@%s:%v/%s?sslmode=disable", password, addr, port, dbName))
	if err != nil {
		return fmt.Errorf("unable to connect to DB: %v", err)
	}

	if opType == "" {
		// applies every migrations till the lastest migration-sql present.
		// Automatically makes sure about the version the current database is on and updates it.
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			return fmt.Errorf("SEVERE: migration could not be applied; %v", err)
		}
		return nil

	} else if opType == "drop_smtable" {
		dbq, err := db.ConnectToDatabaseWithPort(true, port)
		if err != nil {
			return fmt.Errorf("unable to connect to DB: %v", err)
		} else {
			_, err = dbq.Exec("DROP TABLE schema_migrations")
			if err != nil {
				return fmt.Errorf("unable to Drop table: %v", err)
			}
		}
		return nil

	} else if opType == "drop" {
		if err := m.Drop(); err != nil {
			return fmt.Errorf("unable to Drop DB: %v", err)
		}
		return nil

	} else if opType == "downgrade_migration" {
		if err := m.Steps(-1); err != nil {
			return fmt.Errorf("unable to downgrade migration version by 1 level: %v", err)
		}
		return nil
	} else if opType == "upgrade_migration" {
		if err := m.Steps(1); err != nil {
			return fmt.Errorf("unable to upgrade migration version by 1 level: %v", err)
		}
		return nil
	} else if opType == "migrate_to" {
		u64, err := strconv.ParseUint(os.Args[2], 10, 32)
		if err != nil {
			return err
		}
		version := uint(u64)
		if err := m.Migrate(version); err != nil && err != migrate.ErrNoChange {
			return fmt.Errorf("unable to Migrate to version %d: %v", version, err)
		}
		return nil
	} else {
		return fmt.Errorf("invalid argument passed")
	}

}
