// Command datagen seeds a migrated PostgreSQL database for performance
// measurement; see package datagen and `make perf-seed`.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mlops-ca/notarx/tools/datagen"
)

func main() {
	profileName := flag.String("profile", "tiny", "tiny, freetier or target")
	databaseURL := flag.String("database-url", os.Getenv("NOTARX_DATABASE_URL"), "PostgreSQL URL of a migrated, empty database")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	if err := run(context.Background(), *profileName, *databaseURL, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "datagen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, profileName, databaseURL string, seed int64) error {
	profile, err := datagen.ParseProfile(profileName)
	if err != nil {
		return err
	}
	if databaseURL == "" {
		return fmt.Errorf("-database-url or NOTARX_DATABASE_URL is required")
	}
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	start := time.Now()
	counts, err := datagen.Seed(ctx, conn, profile, seed, start)
	if err != nil {
		return err
	}
	// Fresh statistics are what makes the plans of a seeded database meaningful.
	if _, err := conn.Exec(ctx, "ANALYZE"); err != nil {
		return err
	}
	fmt.Printf("seeded profile %s in %s: %d repositories, %d packages, %d versions, %d audit rows\n",
		profile.Name, time.Since(start).Round(time.Second), counts.Repositories, counts.Packages, counts.Versions, counts.AuditRows)
	return nil
}
