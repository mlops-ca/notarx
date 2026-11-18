package config

import "testing"

func TestMigrationPoolIsExplicitOptionalAndWorkerKindIsFilterable(t *testing.T) {
	for key, value := range map[string]string{"NOTARX_DATABASE_URL": "postgres://gateway:test@localhost/gateway_test", "NOTARX_OBJECT_STORE_ENDPOINT": "http://rustfs:9000", "NOTARX_OBJECT_STORE_BUCKET": "test", "NOTARX_OBJECT_STORE_ACCESS_KEY": "test-key", "NOTARX_OBJECT_STORE_SECRET_KEY": "test-secret", "NOTARX_ADMIN_TOKEN": "test-admin", "NOTARX_RESOLVER_TOKEN": "test-resolver", "NOTARX_MIGRATION_STORAGE_POOL_ID": "", "NOTARX_WORKER_KINDS": "migration", "NOTARX_WORKER_FORMATS": "raw"} {
		t.Setenv(key, value)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MigrationStoragePoolID != "" || !cfg.WorkerEnabled("raw", "migration") || cfg.WorkerEnabled("maven", "migration") || cfg.WorkerKindEnabled("replication") {
		t.Fatalf("migration runtime filters=%+v", cfg.WorkerKinds)
	}
	t.Setenv("NOTARX_MIGRATION_STORAGE_POOL_ID", "owned.storage-pool:1")
	cfg, err = Load()
	if err != nil || cfg.MigrationStoragePoolID != "owned.storage-pool:1" {
		t.Fatalf("explicit pool=%q error=%v", cfg.MigrationStoragePoolID, err)
	}
	t.Setenv("NOTARX_MIGRATION_STORAGE_POOL_ID", "secret/invalid-pool")
	if _, err = Load(); err == nil {
		t.Fatal("invalid opaque pool identity was accepted")
	}
}
