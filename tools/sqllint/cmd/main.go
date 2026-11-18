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
	allowlist := flag.String("allowlist", "tools/sqllint/allowlist.txt", "text-cast ratchet allowlist, relative to -root")
	uuidAllowlist := flag.String("uuid-allowlist", "tools/sqllint/allowlist-uuid.txt", "uuid.NewString() ratchet allowlist, relative to -root")
	update := flag.Bool("update", false, "lower the allowlists to the current counts (never raises them)")
	seed := flag.Bool("seed", false, "write the allowlists from the current counts (bootstrap only; it overwrites)")
	flag.Parse()
	os.Exit(run(*root, *allowlist, *uuidAllowlist, *update, *seed))
}

func readAllowlist(path string) (map[string]int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return sqllint.ParseAllowlist(file)
}

func run(root, allowlist, uuidAllowlist string, update, seed bool) int {
	path, uuidPath := root+"/"+allowlist, root+"/"+uuidAllowlist
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
		if err := os.WriteFile(uuidPath, []byte(sqllint.FormatNewStringAllowlist(result.NewStrings)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 2
		}
		return 0
	}
	allow, err := readAllowlist(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sqllint:", err)
		return 2
	}
	uuidAllow, err := readAllowlist(uuidPath)
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
		nextUUID, err := sqllint.LoweredNewStrings(result, uuidAllow)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 1
		}
		if err := os.WriteFile(path, []byte(sqllint.FormatAllowlist(next)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 2
		}
		if err := os.WriteFile(uuidPath, []byte(sqllint.FormatNewStringAllowlist(nextUUID)), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "sqllint:", err)
			return 2
		}
		return 0
	}
	problems := append(sqllint.Check(result, allow), sqllint.CheckNewStrings(result, uuidAllow)...)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}
