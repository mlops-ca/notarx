package migrate

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
)

const usage = `usage: notarx migrate [--dir DIR] [--repair [--completed N]] [--lock-wait DURATION]
       notarx migrate lint [--dir DIR]

Applies the migrations of NOTARX_DATABASE_DRIVER (postgres or mysql) to NOTARX_DATABASE_URL,
or to NOTARX_DATABASE_MIGRATE_URL for mysql when it is set. --dir defaults to
NOTARX_MIGRATIONS_DIR, else migrations/<driver>, else migrations.
The lint subcommand checks that the MySQL migrations after the baseline can be re-run.`

// RunCLI runs "notarx migrate" and returns the process exit code: 0 on
// success, 1 when the run fails, 2 on a usage error.
func RunCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return RunCLIWithEnv(ctx, args, os.Getenv, stdout, stderr)
}

// RunCLIWithEnv is RunCLI with an explicit environment lookup.
func RunCLIWithEnv(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "lint" {
		return runLint(args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", "", "migration directory")
	repairFlag := flags.Bool("repair", false, "resume a half-applied MySQL migration")
	completed := flags.Int("completed", -1, "with --repair: statements of the half-applied migration that took effect")
	lockWait := flags.Duration("lock-wait", DefaultLockWait, "how long to wait for the MySQL migration lock")
	flags.Usage = func() { _, _ = fmt.Fprintln(stderr, usage) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 || (*completed >= 0 && !*repairFlag) || *lockWait <= 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	driver := strings.TrimSpace(getenv("NOTARX_DATABASE_DRIVER"))
	if driver == "" {
		driver = database.DriverPostgres
	}
	if !database.ValidDriver(driver) {
		_, _ = fmt.Fprintln(stderr, "NOTARX_DATABASE_DRIVER must be postgres or mysql")
		return 2
	}
	databaseURL := strings.TrimSpace(getenv("NOTARX_DATABASE_URL"))
	if driver == database.DriverMySQL {
		if migrateURL := strings.TrimSpace(getenv("NOTARX_DATABASE_MIGRATE_URL")); migrateURL != "" {
			databaseURL = migrateURL
		}
	}
	if databaseURL == "" {
		_, _ = fmt.Fprintln(stderr, "NOTARX_DATABASE_URL is not set")
		return 1
	}
	files, err := LoadDir(migrationDir(*dir, driver, getenv))
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	pool := database.PoolConfig{MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Hour, ConnMaxIdleTime: time.Minute, TLSCAFile: strings.TrimSpace(getenv("NOTARX_DATABASE_TLS_CA_FILE"))}
	db, err := database.Open(driver, databaseURL, pool)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot open the database:", database.RedactError(err, databaseURL))
		return 1
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot reach the database:", database.RedactError(err, databaseURL))
		return 1
	}
	opts := Options{Repair: *repairFlag, LockWait: *lockWait, Out: stdout}
	if *completed >= 0 {
		opts.Completed = completed
	}
	if err := Run(ctx, db, driver, files, opts); err != nil {
		_, _ = fmt.Fprintln(stderr, "notarx migrate:", database.RedactError(err, databaseURL))
		return 1
	}
	return 0
}

func migrationDir(flagValue, driver string, getenv func(string) string) string {
	if flagValue != "" {
		return flagValue
	}
	if value := strings.TrimSpace(getenv("NOTARX_MIGRATIONS_DIR")); value != "" {
		return value
	}
	if info, err := os.Stat(filepath.Join("migrations", driver)); err == nil && info.IsDir() {
		return filepath.Join("migrations", driver)
	}
	return "migrations"
}

func runLint(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrate lint", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("dir", filepath.Join("migrations", database.DriverMySQL), "MySQL migration directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	findings, err := LintDir(*dir)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	for _, finding := range findings {
		_, _ = fmt.Fprintln(stderr, finding)
	}
	if len(findings) > 0 {
		_, _ = fmt.Fprintf(stderr, "%d migration finding(s): MySQL migrations after the baseline must be re-runnable (ADR 0022)\n", len(findings))
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "migrate lint: the MySQL migrations in %s are re-runnable\n", *dir)
	return 0
}
