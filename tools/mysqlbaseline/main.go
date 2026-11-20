// Command mysqlbaseline generates the MySQL baseline migration and its decision lists (ADR 0022).
//
//	go run ./tools/mysqlbaseline            write migrations/mysql/000001_baseline.sql and docs/mysql-schema-decisions.md
//	go run ./tools/mysqlbaseline -check     fail when the checked-in files differ from the generated ones
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mlops-ca/notarx/internal/mysqlbaseline"
	"github.com/mlops-ca/notarx/internal/schemaparity"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mysqlbaseline", flag.ContinueOnError)
	flags.SetOutput(stderr)
	catalogue := flags.String("catalogue", "migrations/catalogue.yaml", "neutral schema catalogue")
	decisions := flags.String("decisions", "migrations/mysql-decisions.yaml", "decisions file")
	baseline := flags.String("baseline", "migrations/mysql/000001_baseline.sql", "baseline migration to write")
	doc := flags.String("doc", "docs/mysql-schema-decisions.md", "decision lists to write")
	check := flags.Bool("check", false, "compare instead of writing")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if err := generate(*catalogue, *decisions, *baseline, *doc, *check, stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "mysqlbaseline:", err)
		return 1
	}
	return 0
}

func generate(cataloguePath, decisionsPath, baselinePath, docPath string, check bool, stdout io.Writer) error {
	catalogue, err := schemaparity.Load(cataloguePath)
	if err != nil {
		return err
	}
	decisions, err := mysqlbaseline.LoadDecisions(decisionsPath)
	if err != nil {
		return err
	}
	output, err := mysqlbaseline.Generate(catalogue, decisions)
	if err != nil {
		return fmt.Errorf("the catalogue needs decisions:\n%w", err)
	}
	for path, want := range map[string]string{baselinePath: output.SQL, docPath: output.Doc} {
		if check {
			have, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(have) != want {
				return fmt.Errorf("%s is out of date; regenerate it with: go run ./tools/mysqlbaseline", path)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil { //nolint:gosec // a checked-in source file
			return err
		}
		_, _ = fmt.Fprintf(stdout, "mysqlbaseline: wrote %s\n", path)
	}
	return nil
}
