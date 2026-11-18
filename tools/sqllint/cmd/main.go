// Command sqllint checks the ADR 0023 typed-key rules; see package sqllint.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mlops-ca/notarx/tools/sqllint"
)

func main() {
	root := flag.String("root", ".", "repository root")
	allowlist := flag.String("allowlist", "tools/sqllint/allowlist.txt", "ratchet allowlist, relative to -root")
	update := flag.Bool("update", false, "lower the allowlist to the current counts (never raises it)")
	seed := flag.Bool("seed", false, "write the allowlist from the current counts (bootstrap only; it overwrites)")
	flag.Parse()
	os.Exit(run(*root, *allowlist, *update, *seed))
}

func run(root, allowlist string, update, seed bool) int {
	path := root + "/" + allowlist
	result, err := sqllint.Scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqllint:", err)
		return 2
	}
	if seed {
		if err := os.WriteFile(path, []byte(sqllint.FormatAllowlist(result.Casts)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 2
		}
		return 0
	}
	file, err := os.Open(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqllint:", err)
		return 2
	}
	allow, err := sqllint.ParseAllowlist(file)
	file.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqllint:", err)
		return 2
	}
	if update {
		next, err := sqllint.Lowered(result, allow)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 1
		}
		if err := os.WriteFile(path, []byte(sqllint.FormatAllowlist(next)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 2
		}
		return 0
	}
	problems := sqllint.Check(result, allow)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}
