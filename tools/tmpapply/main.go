// Temporary diagnostic: applies the MySQL baseline statement by statement and reports every failure.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
	"github.com/mlops-ca/notarx/internal/migrate"
)

func main() {
	db, err := database.OpenMySQL(os.Args[1], database.PoolConfig{MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute})
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(1)
	}
	if _, err := db.Exec("DROP DATABASE IF EXISTS diag_baseline"); err != nil {
		fmt.Println("drop:", err)
	}
	if _, err := db.Exec("CREATE DATABASE diag_baseline CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin"); err != nil {
		fmt.Println("create:", err)
		os.Exit(1)
	}
	if _, err := db.Exec("USE diag_baseline"); err != nil {
		fmt.Println("use:", err)
		os.Exit(1)
	}
	data, err := os.ReadFile("migrations/mysql/000001_baseline.sql")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	statements, err := migrate.SplitStatements(string(data))
	if err != nil {
		fmt.Println("split:", err)
		os.Exit(1)
	}
	failed := 0
	for i, s := range statements {
		if _, err := db.Exec(s); err != nil {
			failed++
			head := strings.Join(strings.Fields(s), " ")
			if len(head) > 140 {
				head = head[:140]
			}
			fmt.Printf("DIAG-FAIL statement %d: %v | %s\n", i+1, err, head)
		}
	}
	fmt.Printf("DIAG-DONE %d statements, %d failed\n", len(statements), failed)
}
