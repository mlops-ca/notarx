package dialecttest

import "testing"

func TestPostgresRebindNumbersEveryPlaceholder(t *testing.T) {
	for in, want := range map[string]string{
		"SELECT 1": "SELECT 1", "WHERE a = ?": "WHERE a = $1", "a = ? AND b = ? OR c = ?": "a = $1 AND b = $2 OR c = $3",
	} {
		if got := PostgresRebind(in); got != want {
			t.Errorf("PostgresRebind(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestEqual(t *testing.T) {
	if !equal([]int64{1, 2}, []int64{1, 2}) || equal([]int64{1}, []int64{1, 2}) || equal([]int64{1, 2}, []int64{1, 3}) {
		t.Fatal("equal is wrong")
	}
}
