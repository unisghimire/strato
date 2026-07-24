// Command migrate applies database migrations. Kept as a separate binary so
// deploys can run it as an init container / job with least privilege,
// instead of granting DDL rights to the API server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"

	"github.com/unisghimire/strato/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "configs/config.yaml", "path to config file (empty = env only)")
	source := flag.String("source", "file://migrations", "migration source URL")
	down := flag.Bool("down", false, "roll back one migration instead of migrating up")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	m, err := migrate.New(*source, cfg.Postgres.DSN())
	if err != nil {
		return fmt.Errorf("initializing migrator: %w", err)
	}
	defer m.Close()

	if *down {
		err = m.Steps(-1)
	} else {
		err = m.Up()
	}
	if errors.Is(err, migrate.ErrNoChange) {
		fmt.Println("migrations: no change")
		return nil
	}
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	version, dirty, _ := m.Version()
	fmt.Printf("migrations: ok (version=%d dirty=%v)\n", version, dirty)
	return nil
}
