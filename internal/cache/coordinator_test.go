package cache

import (
	"testing"
	"time"

	"github.com/mlops-ca/notarx/internal/database"
)

func TestPostgresCoordinatorSeparatesLockAndStatePools(t *testing.T) {
	config := database.PoolConfig{MaxOpenConns: 2, MaxIdleConns: 1, ConnMaxLifetime: time.Minute, ConnMaxIdleTime: time.Minute}
	lockDB, err := database.OpenPostgres("postgres://notarx:password@localhost:5432/notarx", config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lockDB.Close() }()
	stateDB, err := database.OpenPostgres("postgres://notarx:password@localhost:5432/notarx", config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stateDB.Close() }()
	coordinator, err := NewPostgresCoordinatorWithPools(lockDB, stateDB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = coordinator.Close() }()
	if coordinator.db != lockDB || coordinator.stateDB != stateDB {
		t.Fatal("coordinator did not retain separate lock and state pools")
	}
}

func TestPostgresCoordinatorRejectsMissingPool(t *testing.T) {
	if _, err := NewPostgresCoordinatorWithPools(nil, nil); err == nil {
		t.Fatal("NewPostgresCoordinatorWithPools() error=nil, want error")
	}
}
