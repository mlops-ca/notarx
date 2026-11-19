package database

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMySQLURLAcceptsTheURLForm(t *testing.T) {
	cfg, mode, err := ParseMySQLURL("mysql://notarx:p%40ss@db.internal:3307/notarx?tls=verify-full", "")
	if err != nil {
		t.Fatal(err)
	}
	if mode != TLSVerifyFull || cfg.TLS == nil || cfg.TLS.ServerName != "db.internal" || cfg.TLS.InsecureSkipVerify {
		t.Fatalf("mode %q tls %#v", mode, cfg.TLS)
	}
	if cfg.User != "notarx" || cfg.Passwd != "p@ss" || cfg.Addr != "db.internal:3307" || cfg.DBName != "notarx" || cfg.Net != "tcp" {
		t.Fatalf("identity fields: %#v", cfg)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || cfg.ClientFoundRows || cfg.MultiStatements || cfg.Collation != "" {
		t.Fatalf("fixed parameters: %#v", cfg)
	}
	for key, want := range map[string]string{
		"collation_connection":     "'utf8mb4_0900_bin'",
		"time_zone":                "'+00:00'",
		"transaction_isolation":    "'READ-COMMITTED'",
		"innodb_lock_wait_timeout": "30",
	} {
		if cfg.Params[key] != want {
			t.Fatalf("%s = %q, want %q", key, cfg.Params[key], want)
		}
	}
	if !strings.Contains(cfg.Params["sql_mode"], "STRICT_ALL_TABLES") || !strings.Contains(cfg.Params["sql_mode"], "ONLY_FULL_GROUP_BY") {
		t.Fatalf("sql_mode = %q", cfg.Params["sql_mode"])
	}
}

func TestParseMySQLURLDefaultsTLSByHost(t *testing.T) {
	cfg, mode, err := ParseMySQLURL("mysql://u:p@db.internal/notarx", "")
	if err != nil || mode != TLSVerifyFull || cfg.Addr != "db.internal:3306" {
		t.Fatalf("remote default: %q %v %v", mode, cfg, err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		cfg, mode, err := ParseMySQLURL("mysql://u:p@"+host+"/notarx", "")
		if err != nil || mode != "" || cfg.TLS != nil {
			t.Fatalf("%s: mode %q tls %v err %v", host, mode, cfg.TLS, err)
		}
	}
	cfg, mode, err = ParseMySQLURL("mysql://u:p@db/notarx?tls=required", "")
	if err != nil || mode != TLSRequired || !cfg.TLS.InsecureSkipVerify {
		t.Fatalf("required: %q %v", mode, err)
	}
}

func TestParseMySQLURLRejectsEveryBadForm(t *testing.T) {
	const secret = "hunter2-secret"
	cases := map[string]string{
		"empty":            "",
		"driver DSN":       "notarx:" + secret + "@tcp(db:3306)/notarx",
		"postgres scheme":  "postgres://u:" + secret + "@db/notarx",
		"no user":          "mysql://db/notarx",
		"empty user":       "mysql://:" + secret + "@db/notarx",
		"no host":          "mysql://u:" + secret + "@/notarx",
		"no database":      "mysql://u:" + secret + "@db",
		"nested path":      "mysql://u:" + secret + "@db/a/b",
		"fragment":         "mysql://u:" + secret + "@db/notarx#x",
		"driver parameter": "mysql://u:" + secret + "@db/notarx?parseTime=false",
		"collation":        "mysql://u:" + secret + "@db/notarx?collation=utf8mb4_general_ci",
		"unknown tls":      "mysql://u:" + secret + "@db/notarx?tls=skip-verify",
		"repeated tls":     "mysql://u:" + secret + "@db/notarx?tls=required&tls=verify-full",
		"unparseable":      "mysql://u:" + secret + "@db:notaport/notarx%zz",
	}
	for name, raw := range cases {
		_, _, err := ParseMySQLURL(raw, "")
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error leaks the password: %v", name, err)
		}
	}
}

func TestParseMySQLURLChecksTheCAFile(t *testing.T) {
	dir := t.TempDir()
	ca := writeTestCA(t, dir)
	if _, mode, err := ParseMySQLURL("mysql://u:p@db/notarx?tls=verify-ca", ca); err != nil || mode != TLSVerifyCA {
		t.Fatalf("verify-ca: %q %v", mode, err)
	}
	cfg, _, err := ParseMySQLURL("mysql://u:p@db/notarx?tls=verify-full", ca)
	if err != nil || cfg.TLS.RootCAs == nil {
		t.Fatalf("verify-full with CA: %v", err)
	}
	bad := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(bad, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][2]string{
		"missing file":      {"mysql://u:p@db/notarx", filepath.Join(dir, "absent.pem")},
		"not PEM":           {"mysql://u:p@db/notarx", bad},
		"plaintext with CA": {"mysql://u:p@localhost/notarx", ca},
		"required with CA":  {"mysql://u:p@db/notarx?tls=required", ca},
	} {
		if _, _, err := ParseMySQLURL(args[0], args[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVerifyCAChecksTheChainButNotTheHostName(t *testing.T) {
	dir := t.TempDir()
	caPath := writeTestCA(t, dir)
	pool := x509.NewCertPool()
	data, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(caCert)
	verify := verifyChainOnly(pool)
	if err := verify([][]byte{caCert.Raw}, nil); err != nil {
		t.Fatalf("a certificate signed by the trusted CA was rejected: %v", err)
	}
	if err := verify(nil, nil); err == nil {
		t.Fatal("no certificate accepted")
	}
	if err := verify([][]byte{[]byte("garbage")}, nil); err == nil {
		t.Fatal("garbage certificate accepted")
	}
	other := x509.NewCertPool()
	if err := verifyChainOnly(other)([][]byte{caCert.Raw}, nil); err == nil {
		t.Fatal("certificate verified against an empty root pool")
	}
}

func writeTestCA(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "notarx test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenSelectsTheDriverAndBoundsThePool(t *testing.T) {
	config := PoolConfig{MaxOpenConns: 3, MaxIdleConns: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute}
	db, err := Open(DriverMySQL, "mysql://u:p@localhost/notarx", config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if db.Stats().MaxOpenConnections != 3 {
		t.Fatalf("stats = %#v", db.Stats())
	}
	for _, driver := range []string{"", DriverPostgres} {
		pg, err := Open(driver, "postgres://u:p@localhost:5432/notarx?sslmode=disable", config)
		if err != nil {
			t.Fatal(err)
		}
		_ = pg.Close()
	}
	pg, err := Open(DriverPostgres, "postgres://u:p@localhost:5432/notarx?sslmode=disable", config)
	if err != nil {
		t.Fatal(err)
	}
	_ = pg.Close()
	for name, run := range map[string]func() error{
		"unknown driver": func() error { _, err := Open("oracle", "x", config); return err },
		"bad pool":       func() error { _, err := Open(DriverMySQL, "mysql://u:p@localhost/n", PoolConfig{}); return err },
		"driver DSN":     func() error { _, err := Open(DriverMySQL, "u:p@tcp(db)/n", config); return err },
	} {
		if run() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if !ValidDriver("mysql") || !ValidDriver("postgres") || ValidDriver("sqlite") {
		t.Fatal("ValidDriver")
	}
}

func TestRedactionHidesPasswords(t *testing.T) {
	raw := "mysql://notarx:s3cret@db:3306/notarx"
	if got := RedactURL(raw); strings.Contains(got, "s3cret") || !strings.Contains(got, "notarx:xxxxx@db") {
		t.Fatalf("RedactURL = %q", got)
	}
	encoded := "mysql://notarx:p%40ss%2Fword@db/notarx"
	if got := RedactError(errors.New("auth failed for p%40ss%2Fword and p@ss/word"), encoded); strings.Contains(got, "ss") && strings.Contains(got, "word") {
		t.Fatalf("encoded password: %q", got)
	}
	if got := RedactError(errors.New("abc"), ""); got != "abc" {
		t.Fatalf("empty URL mangled the message: %q", got)
	}
	if got := RedactError(errors.New("a1b1"), "mysql://u:1@db/n"); got != "a1b1" {
		t.Fatalf("short password mangled the message: %q", got)
	}
	if got := RedactURL("mysql://db/notarx"); got != "mysql://db/notarx" {
		t.Fatalf("no password: %q", got)
	}
	if got := RedactURL("mysql://u:p@db/%zz"); strings.Contains(got, "p@") {
		t.Fatalf("unparseable: %q", got)
	}
	err := errors.New("dial " + raw + " failed: access denied for password s3cret")
	if got := RedactError(err, raw); strings.Contains(got, "s3cret") {
		t.Fatalf("RedactError = %q", got)
	}
	if RedactError(nil, raw) != "" {
		t.Fatal("nil error")
	}
}

func TestSplitPoolBudget(t *testing.T) {
	if DefaultPoolSet(DriverPostgres).Total() != 46 || DefaultPoolSet(DriverMySQL).Total() != 44 {
		t.Fatal("default budgets are 46 on PostgreSQL and 44 on MySQL")
	}
	for _, driver := range []string{DriverPostgres, DriverMySQL} {
		pools := 4
		if driver == DriverMySQL {
			pools = 3
		}
		for total := pools; total <= 200; total++ {
			set, err := SplitPoolBudget(driver, total)
			if err != nil {
				t.Fatal(err)
			}
			if set.Total() != total {
				t.Fatalf("%s %d: total %d", driver, total, set.Total())
			}
			for _, pool := range []PoolConfig{set.Main, set.Coordinator, set.ArtifactLock} {
				if pool.MaxOpenConns < 1 || pool.MaxIdleConns > pool.MaxOpenConns {
					t.Fatalf("%s %d: %#v", driver, total, pool)
				}
			}
			if driver == DriverMySQL && set.Notification.MaxOpenConns != 0 {
				t.Fatal("MySQL opens no notification pool")
			}
			if driver == DriverPostgres && set.Notification.MaxOpenConns < 1 {
				t.Fatal("PostgreSQL keeps a notification connection")
			}
		}
	}
	exact, _ := SplitPoolBudget(DriverPostgres, 46)
	if exact.Main.MaxOpenConns != 32 || exact.Coordinator.MaxOpenConns != 8 || exact.ArtifactLock.MaxOpenConns != 4 || exact.Notification.MaxOpenConns != 2 {
		t.Fatalf("46 on PostgreSQL = %#v", exact)
	}
	exact, _ = SplitPoolBudget(DriverMySQL, 44)
	if exact.Main.MaxOpenConns != 32 || exact.Coordinator.MaxOpenConns != 8 || exact.ArtifactLock.MaxOpenConns != 4 {
		t.Fatalf("44 on MySQL = %#v", exact)
	}
	if _, err := SplitPoolBudget(DriverPostgres, 3); err == nil {
		t.Fatal("a budget below one connection per pool was accepted")
	}
}
