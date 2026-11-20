// Command perfload runs the mixed load profile against a gateway and prints the JSON
// report; see package perfload and `make perf-load`.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/jackc/pgx/v5"

	"github.com/mlops-ca/notarx/tools/perfload"
)

func main() {
	profile := flag.String("profile", "tiny", "tiny, freetier or target")
	baseURL := flag.String("url", "", "gateway base URL")
	databaseURL := flag.String("database-url", os.Getenv("NOTARX_DATABASE_URL"), "seeded PostgreSQL database, read for the packages to request")
	publishRepository := flag.String("publish-repository", "", "hosted raw repository that accepts the publishes")
	output := flag.String("output", "", "file to write the report to (default stdout)")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, *profile, *baseURL, *databaseURL, *publishRepository, *output); err != nil {
		fmt.Fprintln(os.Stderr, "perfload:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, profile, baseURL, databaseURL, publishRepository, output string) error {
	sizes, err := perfload.ProfileSizes(profile)
	if err != nil {
		return err
	}
	token := os.Getenv("NOTARX_ADMIN_TOKEN")
	if token == "" || baseURL == "" || databaseURL == "" {
		return fmt.Errorf("-url, -database-url (or NOTARX_DATABASE_URL) and NOTARX_ADMIN_TOKEN are required")
	}
	targets, err := loadTargets(ctx, databaseURL)
	if err != nil {
		return err
	}
	report, err := perfload.Run(ctx, perfload.Config{
		Profile: profile, BaseURL: baseURL, Token: token, Targets: targets,
		PublishRepository: publishRepository, Sizes: sizes, Seed: 1,
	})
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if output == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(output, data, 0o644)
}

// packagesPerRepository bounds how many packages of one repository become read targets, so the
// targets cover every seeded repository, hottest first, instead of the first one alone.
const packagesPerRepository = 50

// loadTargets returns packages of the datagen repositories, hottest repository first:
// datagen places packages by popularity, so a repository with a lower number holds more.
func loadTargets(ctx context.Context, databaseURL string) ([]perfload.Target, error) {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `
		SELECT repo, pkg FROM (
			SELECT r.name AS repo, p.name AS pkg,
			       row_number() OVER (PARTITION BY r.id ORDER BY p.name) AS rank
			  FROM hosted_repositories r
			  JOIN native_npm_packages p ON p.repository_id = r.id
			 WHERE r.name LIKE 'perf-npm-%'
		) ranked
		 WHERE rank <= $1
		 ORDER BY repo, pkg`, packagesPerRepository)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []perfload.Target
	for rows.Next() {
		var target perfload.Target
		if err := rows.Scan(&target.Repository, &target.Package); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}
