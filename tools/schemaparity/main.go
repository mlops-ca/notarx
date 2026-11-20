// Command schemaparity generates and checks the dialect-neutral schema catalogue (ADR 0022).
//
//	go run ./tools/schemaparity generate [-postgres-url URL] [-out migrations/catalogue.yaml]
//	go run ./tools/schemaparity check    [-postgres-url URL] [-mysql-url URL] [-catalogue migrations/catalogue.yaml]
//
// generate introspects a migrated PostgreSQL and writes the catalogue. check compares the
// catalogue with a migrated PostgreSQL and, when -mysql-url is given, with a migrated MySQL
// (mysql:// URL form), and exits 1 on any drift. -postgres-url defaults to TEST_DATABASE_URL.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/schemaparity"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

const usage = "usage: schemaparity generate|check [flags]"

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet("schemaparity "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	postgresURL := flags.String("postgres-url", getenv("TEST_DATABASE_URL"), "migrated PostgreSQL URL")
	mysqlURL := flags.String("mysql-url", "", "migrated MySQL URL (mysql:// form); omit to skip the MySQL comparison")
	path := flags.String("catalogue", "migrations/catalogue.yaml", "catalogue file")
	flags.StringVar(path, "out", "migrations/catalogue.yaml", "catalogue file to write (generate)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "generate":
		err = generate(ctx, *postgresURL, *path, stdout)
	case "check":
		err = check(ctx, *postgresURL, *mysqlURL, *path, stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "schemaparity:", err)
		return 1
	}
	return 0
}

var pool = database.PoolConfig{MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Hour, ConnMaxIdleTime: time.Minute}

func open(ctx context.Context, driver, url string) (*sql.DB, error) {
	if url == "" {
		return nil, fmt.Errorf("no %s URL: set -%s-url", driver, driver)
	}
	db, err := database.Open(driver, url, pool)
	if err != nil {
		return nil, errors.New(database.RedactError(err, url))
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot reach the %s database: %s", driver, database.RedactError(err, url))
	}
	return db, nil
}

func generate(ctx context.Context, postgresURL, path string, stdout io.Writer) error {
	db, err := open(ctx, database.DriverPostgres, postgresURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	catalogue, err := schemaparity.Introspect(ctx, db, schemaparity.DialectPostgres)
	if err != nil {
		return err
	}
	if catalogue.Seeds, err = schemaparity.IntrospectSeeds(ctx, db, schemaparity.DialectPostgres, catalogue); err != nil {
		return err
	}
	data, err := schemaparity.Marshal(catalogue)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // a checked-in source file
		return err
	}
	_, _ = fmt.Fprintf(stdout, "schemaparity: wrote %s (%d tables)\n", path, len(catalogue.Tables))
	return nil
}

func check(ctx context.Context, postgresURL, mysqlURL, path string, stdout, stderr io.Writer) error {
	want, err := schemaparity.Load(path)
	if err != nil {
		return err
	}
	targets := []struct{ driver, url string }{{database.DriverPostgres, postgresURL}}
	if mysqlURL != "" {
		targets = append(targets, struct{ driver, url string }{database.DriverMySQL, mysqlURL})
	}
	failed := false
	for _, target := range targets {
		problems, err := checkOne(ctx, want, target.driver, target.url)
		if err != nil {
			return err
		}
		for _, problem := range problems {
			_, _ = fmt.Fprintf(stderr, "%s: %s\n", target.driver, problem)
		}
		if len(problems) == 0 {
			_, _ = fmt.Fprintf(stdout, "schemaparity: %s matches the catalogue (%d tables)\n", target.driver, len(want.Tables))
		}
		failed = failed || len(problems) > 0
	}
	if failed {
		return errors.New("the schema drifted from the catalogue; fix the migrations, or regenerate it with: go run ./tools/schemaparity generate")
	}
	return nil
}

func checkOne(ctx context.Context, want schemaparity.Catalogue, driver, url string) ([]string, error) {
	db, err := open(ctx, driver, url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	got, err := schemaparity.Introspect(ctx, db, driver)
	if err != nil {
		return nil, err
	}
	problems := schemaparity.Compare(want, got, driver)
	seeds, err := schemaparity.IntrospectSeeds(ctx, db, driver, want)
	if err != nil {
		return nil, err
	}
	return append(problems, schemaparity.CompareSeeds(want.Seeds, seeds)...), nil
}
