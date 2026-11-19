package database

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Driver names accepted by NOTARX_DATABASE_DRIVER.
const (
	DriverPostgres = "postgres"
	DriverMySQL    = "mysql"
)

// MySQL TLS modes accepted in the tls query parameter of a mysql:// URL. They
// are notarx's own names, not the go-sql-driver ones.
const (
	TLSRequired   = "required"
	TLSVerifyCA   = "verify-ca"
	TLSVerifyFull = "verify-full"
)

const (
	mysqlCollation = "utf8mb4_0900_bin"
	mysqlSQLMode   = "STRICT_ALL_TABLES,NO_ZERO_DATE,NO_ZERO_IN_DATE,ERROR_FOR_DIVISION_BY_ZERO,ONLY_FULL_GROUP_BY,NO_ENGINE_SUBSTITUTION"
	// MySQLLockWaitTimeoutSeconds bounds innodb_lock_wait_timeout for every session.
	MySQLLockWaitTimeoutSeconds = 30
)

// ValidDriver reports whether name is a supported NOTARX_DATABASE_DRIVER value.
func ValidDriver(name string) bool {
	return name == DriverPostgres || name == DriverMySQL
}

// Open returns a bounded pool for the selected driver. PostgreSQL behaviour is
// that of OpenPostgres; the MySQL pool uses the fixed session parameters of
// ADR 0022 and refuses the driver DSN form.
func Open(driver, databaseURL string, config PoolConfig) (*sql.DB, error) {
	switch driver {
	case DriverPostgres:
		return OpenPostgres(databaseURL, config)
	case DriverMySQL:
		return OpenMySQL(databaseURL, config)
	default:
		return nil, fmt.Errorf("unsupported database driver %q", driver)
	}
}

// OpenMySQL opens a bounded MySQL pool from a mysql:// URL. config.TLSCAFile
// may be empty, which selects the system roots.
func OpenMySQL(databaseURL string, config PoolConfig) (*sql.DB, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	cfg, mode, err := ParseMySQLURL(databaseURL, config.TLSCAFile)
	if err != nil {
		return nil, err
	}
	if mode == TLSRequired {
		slog.Warn("MySQL TLS mode required does not verify the server certificate; use tls=verify-ca or tls=verify-full")
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, errors.New("database configuration is invalid")
	}
	db := sql.OpenDB(connector)
	ConfigurePool(db, config)
	return db, nil
}

// ParseMySQLURL converts the mysql://user:password@host:3306/name?tls=verify-full
// form into a go-sql-driver configuration and returns the effective TLS mode
// ("" for a plaintext loopback connection). Errors never echo the URL, so a
// password cannot leak through them. Only the tls query parameter is accepted:
// the connection parameters of ADR 0022 are fixed by notarx.
func ParseMySQLURL(raw, caFile string) (*mysql.Config, string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, "", errors.New("the MySQL URL is empty")
	}
	if !strings.HasPrefix(raw, "mysql://") {
		return nil, "", errors.New("the MySQL URL must use the mysql:// form; the driver DSN form is not accepted")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, "", errors.New("the MySQL URL is not valid")
	}
	if parsed.User == nil || parsed.User.Username() == "" {
		return nil, "", errors.New("the MySQL URL must contain a user name")
	}
	host := parsed.Hostname()
	if host == "" {
		return nil, "", errors.New("the MySQL URL must contain a host")
	}
	port := parsed.Port()
	if port == "" {
		port = "3306"
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" || strings.Contains(name, "/") {
		return nil, "", errors.New("the MySQL URL must name exactly one database")
	}
	if parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, "", errors.New("the MySQL URL must not contain a fragment")
	}
	query := parsed.Query()
	for key := range query {
		if key != "tls" {
			return nil, "", fmt.Errorf("the MySQL URL parameter %q is not accepted; only tls is configurable", key)
		}
	}
	if len(query["tls"]) > 1 {
		return nil, "", errors.New("the MySQL URL tls parameter must appear once")
	}
	mode := query.Get("tls")
	loopback := isLoopbackHost(host)
	switch mode {
	case "":
		if !loopback {
			mode = TLSVerifyFull
		}
	case TLSRequired, TLSVerifyCA, TLSVerifyFull:
	default:
		return nil, "", errors.New("the MySQL URL tls parameter must be required, verify-ca or verify-full")
	}
	if mode == "" && caFile != "" {
		return nil, "", errors.New("a database TLS CA file needs a TLS mode other than plaintext")
	}
	if mode == TLSRequired && caFile != "" {
		return nil, "", errors.New("a database TLS CA file has no effect with tls=required; use verify-ca or verify-full")
	}

	cfg := mysql.NewConfig()
	cfg.User = parsed.User.Username()
	cfg.Passwd, _ = parsed.User.Password()
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, port)
	cfg.DBName = name
	cfg.Collation = mysqlCollation
	cfg.Loc = time.UTC
	cfg.ParseTime = true
	cfg.ClientFoundRows = false
	cfg.MultiStatements = false
	cfg.Params = map[string]string{
		"time_zone":                "'+00:00'",
		"transaction_isolation":    "'READ-COMMITTED'",
		"sql_mode":                 "'" + mysqlSQLMode + "'",
		"innodb_lock_wait_timeout": fmt.Sprint(MySQLLockWaitTimeoutSeconds),
	}
	if mode != "" {
		tlsConfig, err := mysqlTLSConfig(mode, host, caFile)
		if err != nil {
			return nil, "", err
		}
		cfg.TLS = tlsConfig
	}
	return cfg, mode, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func mysqlTLSConfig(mode, host, caFile string) (*tls.Config, error) {
	var roots *x509.CertPool
	if caFile != "" {
		data, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("the database TLS CA file cannot be read")
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return nil, errors.New("the database TLS CA file contains no PEM certificate")
		}
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	switch mode {
	case TLSRequired:
		config.InsecureSkipVerify = true //nolint:gosec // explicit, documented no-verify mode
	case TLSVerifyFull:
		config.ServerName = host
	case TLSVerifyCA:
		// crypto/tls has no CA-only mode: skip its host check and verify the chain
		// ourselves without a server name.
		config.InsecureSkipVerify = true //nolint:gosec // chain verified in VerifyPeerCertificate
		config.VerifyPeerCertificate = verifyChainOnly(roots)
	}
	return config, nil
}

func verifyChainOnly(roots *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("the database server presented no certificate")
		}
		certs := make([]*x509.Certificate, 0, len(rawCerts))
		for _, raw := range rawCerts {
			cert, err := x509.ParseCertificate(raw)
			if err != nil {
				return errors.New("the database server certificate cannot be parsed")
			}
			certs = append(certs, cert)
		}
		intermediates := x509.NewCertPool()
		for _, cert := range certs[1:] {
			intermediates.AddCert(cert)
		}
		_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		return err
	}
}

// RedactURL replaces the password of a database URL so it can be logged. A URL
// that cannot be parsed is replaced entirely.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		if err != nil {
			return "[unparseable database URL]"
		}
		return raw
	}
	if _, set := parsed.User.Password(); set {
		parsed.User = url.UserPassword(parsed.User.Username(), "xxxxx")
	}
	return parsed.String()
}

// RedactError removes every occurrence of the URL, and of its password, from
// an error message produced by a driver.
func RedactError(err error, raw string) string {
	if err == nil {
		return ""
	}
	message := strings.ReplaceAll(err.Error(), raw, RedactURL(raw))
	if parsed, parseErr := url.Parse(raw); parseErr == nil && parsed.User != nil {
		if password, set := parsed.User.Password(); set && password != "" {
			message = strings.ReplaceAll(message, password, "xxxxx")
		}
	}
	return message
}

// PoolSet is the connection budget split across the pools one process opens.
type PoolSet struct {
	Main         PoolConfig
	Coordinator  PoolConfig
	ArtifactLock PoolConfig
	// Notification is the zero value on MySQL, where no LISTEN pool is opened.
	Notification PoolConfig
}

// Total is the number of connections the set may hold open.
func (s PoolSet) Total() int {
	return s.Main.MaxOpenConns + s.Coordinator.MaxOpenConns + s.ArtifactLock.MaxOpenConns + s.Notification.MaxOpenConns
}

// DefaultPoolSet is today's 32:8:4:2 split on PostgreSQL (46) and 32:8:4 on
// MySQL (44).
func DefaultPoolSet(driver string) PoolSet {
	set := PoolSet{Main: DefaultPoolConfig(), Coordinator: DefaultCoordinatorPoolConfig(), ArtifactLock: DefaultArtifactLockPoolConfig()}
	if driver != DriverMySQL {
		set.Notification = NotificationPoolConfig()
	}
	return set
}

// SplitPoolBudget divides total connections across the pools in the 32:8:4
// ratio (plus the 2-connection notification pool on PostgreSQL), at least one
// per pool, using the largest-remainder method so the pools add up to total.
func SplitPoolBudget(driver string, total int) (PoolSet, error) {
	set := DefaultPoolSet(driver)
	weights := []int{set.Main.MaxOpenConns, set.Coordinator.MaxOpenConns, set.ArtifactLock.MaxOpenConns}
	if driver != DriverMySQL {
		weights = append(weights, set.Notification.MaxOpenConns)
	}
	if total < len(weights) {
		return PoolSet{}, fmt.Errorf("the database connection budget must be at least %d (one per pool)", len(weights))
	}
	weightSum := 0
	for _, weight := range weights {
		weightSum += weight
	}
	shares := make([]int, len(weights))
	remainders := make([]int, len(weights))
	assigned := 0
	for i, weight := range weights {
		shares[i] = total * weight / weightSum
		remainders[i] = total*weight - shares[i]*weightSum
		if shares[i] < 1 {
			shares[i], remainders[i] = 1, 0
		}
		assigned += shares[i]
	}
	for assigned < total {
		best := 0
		for i := range remainders {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		shares[best]++
		remainders[best] = -1
		assigned++
	}
	for assigned > total {
		widest := 0
		for i := range shares {
			if shares[i] > shares[widest] {
				widest = i
			}
		}
		shares[widest]--
		assigned--
	}
	apply := func(pool PoolConfig, open int) PoolConfig {
		pool.MaxOpenConns = open
		pool.MaxIdleConns = min(pool.MaxIdleConns, open)
		return pool
	}
	set.Main = apply(set.Main, shares[0])
	set.Coordinator = apply(set.Coordinator, shares[1])
	set.ArtifactLock = apply(set.ArtifactLock, shares[2])
	if driver != DriverMySQL {
		set.Notification = apply(set.Notification, shares[3])
	}
	return set, nil
}
