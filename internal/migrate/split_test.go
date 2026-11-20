package migrate

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	cases := map[string]struct {
		in   string
		want []string
	}{
		"two statements":         {"CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);", []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"}},
		"no trailing semicolon":  {"SELECT 1", []string{"SELECT 1"}},
		"empty units dropped":    {";;  ;SELECT 1;;", []string{"SELECT 1"}},
		"semicolon in string":    {"INSERT INTO t VALUES ('a;b'); SELECT 2;", []string{"INSERT INTO t VALUES ('a;b')", "SELECT 2"}},
		"semicolon in double":    {`INSERT INTO t VALUES ("a;b");`, []string{`INSERT INTO t VALUES ("a;b")`}},
		"doubled quote":          {"SELECT 'it''s; ok'; SELECT 2", []string{"SELECT 'it''s; ok'", "SELECT 2"}},
		"backslash escape":       {"SELECT 'a\\'; b'; SELECT 3", []string{"SELECT 'a\\'; b'", "SELECT 3"}},
		"quoted identifier":      {"SELECT `a;b` FROM `c``;d`; SELECT 4", []string{"SELECT `a;b` FROM `c``;d`", "SELECT 4"}},
		"line comments":          {"-- a; b\nSELECT 1; # c; d\nSELECT 2; --\nSELECT 3", []string{"SELECT 1", "SELECT 2", "SELECT 3"}},
		"dash without space":     {"SELECT 1--1; SELECT 2", []string{"SELECT 1--1", "SELECT 2"}},
		"block comment":          {"/* a; b */ SELECT 1 /* c; */; SELECT 2", []string{"SELECT 1", "SELECT 2"}},
		"comment only":           {"-- nothing\n/* nothing */", []string{}},
		"executable comment":     {"/*!40101 SET NAMES utf8mb4 */; SELECT 1", []string{"/*!40101 SET NAMES utf8mb4 */", "SELECT 1"}},
		"carriage returns":       {"SELECT 1;\r\nSELECT 2;\r\n", []string{"SELECT 1", "SELECT 2"}},
		"trigger one statement":  {"CREATE TRIGGER IF NOT EXISTS t BEFORE INSERT ON a FOR EACH ROW SET NEW.b = 1;", []string{"CREATE TRIGGER IF NOT EXISTS t BEFORE INSERT ON a FOR EACH ROW SET NEW.b = 1"}},
		"non-ascii identifier":   {"SELECT é FROM t;", []string{"SELECT é FROM t"}},
		"keyword in a string":    {"SELECT 'DELIMITER //'; SELECT 1", []string{"SELECT 'DELIMITER //'", "SELECT 1"}},
		"procedure in a comment": {"/* CREATE PROCEDURE p() BEGIN END */ SELECT 1", []string{"SELECT 1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := SplitStatements(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
}

func TestSplitStatementsRejects(t *testing.T) {
	cases := map[string]string{
		"delimiter":              "DELIMITER //\nSELECT 1//",
		"delimiter after unit":   "SELECT 1; delimiter $$",
		"procedure":              "CREATE PROCEDURE p() SELECT 1;",
		"definer function":       "CREATE DEFINER=`root`@`%` FUNCTION f() RETURNS INT RETURN 1;",
		"event":                  "CREATE EVENT e ON SCHEDULE EVERY 1 DAY DO SELECT 1;",
		"trigger with begin":     "CREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW BEGIN SET NEW.b = 1; END;",
		"unterminated string":    "SELECT 'abc",
		"unterminated backtick":  "SELECT `abc",
		"unterminated block":     "SELECT 1 /* abc",
		"unterminated backslash": "SELECT 'abc\\",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if got, err := SplitStatements(in); err == nil {
				t.Fatalf("accepted: %q", got)
			}
		})
	}
}

func TestStripDownCutsAtTheMarker(t *testing.T) {
	cases := map[string]string{
		"-- +goose Up\nSELECT 1;\n-- +goose Down\nDROP TABLE a;\n": "-- +goose Up\nSELECT 1;\n",
		"SELECT 1;\n":                          "SELECT 1;\n",
		"SELECT 1;\n  -- +goose Down extra\nX": "SELECT 1;\n",
		"-- +goose Down\nX":                    "",
	}
	for in, want := range cases {
		if got := StripDown(in); got != want {
			t.Fatalf("StripDown(%q) = %q; want %q", in, got, want)
		}
	}
	if !strings.HasSuffix(StripDown("a\n-- +goose Down"), "a\n") {
		t.Fatal("a marker on the last line without a newline is not cut")
	}
}
