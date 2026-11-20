package cache

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestLeaseTable(t *testing.T) {
	table := newLeaseTable()
	if _, ok := table.take("missing"); ok {
		t.Fatal("took a lease that does not exist")
	}
	if renewed, err := table.renew(context.Background(), "missing"); renewed || err != nil {
		t.Fatalf("renew of a missing lease = %t, %v", renewed, err)
	}
	table.add("a", sessionLease{key: "ka"})
	table.add("b", sessionLease{key: "kb"})
	if lease, ok := table.take("a"); !ok || lease.key != "ka" {
		t.Fatalf("take = %+v, %t", lease, ok)
	}
	if _, ok := table.take("a"); ok {
		t.Fatal("a lease was taken twice")
	}
	drained := table.drain()
	if len(drained) != 1 || drained[0].key != "kb" || len(table.drain()) != 0 {
		t.Fatalf("drain = %+v", drained)
	}
}

func TestCloseLeaseReportsOnlyTheUnlockError(t *testing.T) {
	boom := errors.New("unlock failed")
	for name, unlockErr := range map[string]error{"clean": nil, "failed unlock": boom} {
		t.Run(name, func(t *testing.T) {
			server := mysqlLockServer(true, int64(1))
			conn, err := server.Open(t).Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got := closeLease(conn, unlockErr)
			if !errors.Is(got, unlockErr) && (unlockErr != nil || got != nil) {
				t.Fatalf("closeLease = %v; want %v", got, unlockErr)
			}
			if errors.Is(got, sql.ErrConnDone) {
				t.Fatal("a discarded connection reported that it was already closed, which hides the cause")
			}
		})
	}
}
