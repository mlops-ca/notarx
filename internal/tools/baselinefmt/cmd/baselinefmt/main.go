// Command baselinefmt reads a schema-only pg_dump on stdin and writes the
// normalised baseline migration on stdout.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mlops-ca/notarx/internal/tools/baselinefmt"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	dump, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	baseline, err := baselinefmt.Normalize(dump)
	if err != nil {
		return err
	}
	_, err = out.Write(baseline)
	return err
}
