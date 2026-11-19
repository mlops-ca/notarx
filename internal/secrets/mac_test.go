package secrets

import (
	"errors"
	"testing"
)

func TestMACRoundTripAndTamper(t *testing.T) {
	t.Setenv(KeyEnv, "0123456789abcdef0123456789abcdef")
	t.Setenv(KeyPreviousEnv, "")
	mac, err := MAC("purpose", "a", "bc")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyMAC("purpose", mac, "a", "bc"); err != nil {
		t.Fatal(err)
	}
	for name, fields := range map[string][]string{"moved boundary": {"ab", "c"}, "changed": {"a", "bd"}, "extra": {"a", "bc", ""}} {
		if err := VerifyMAC("purpose", mac, fields...); !errors.Is(err, ErrMACInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := VerifyMAC("other", mac, "a", "bc"); !errors.Is(err, ErrMACInvalid) {
		t.Errorf("purpose must be bound: %v", err)
	}
	for _, bad := range []string{"", "m1:", "m1:zz:abc", "x"} {
		if err := VerifyMAC("purpose", bad, "a", "bc"); !errors.Is(err, ErrMACInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestMACSurvivesRotationUntilKeyDropped(t *testing.T) {
	const keyA, keyB = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"
	t.Setenv(KeyEnv, keyA)
	t.Setenv(KeyPreviousEnv, "")
	mac, err := MAC("p", "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyEnv, keyB)
	t.Setenv(KeyPreviousEnv, keyA)
	if err := VerifyMAC("p", mac, "x"); err != nil {
		t.Fatalf("retired key must still verify: %v", err)
	}
	if need, err := MACNeedsRefresh(mac); err != nil || !need {
		t.Fatalf("need=%v err=%v", need, err)
	}
	fresh, _ := MAC("p", "x")
	if need, _ := MACNeedsRefresh(fresh); need {
		t.Fatal("a current MAC needs no refresh")
	}
	t.Setenv(KeyPreviousEnv, "")
	if err := VerifyMAC("p", mac, "x"); !errors.Is(err, ErrMACInvalid) {
		t.Fatalf("dropped key must fail closed: %v", err)
	}
}

func TestMACRequiresKey(t *testing.T) {
	t.Setenv(KeyEnv, "")
	if _, err := MAC("p", "x"); !errors.Is(err, ErrKeyNotConfigured) {
		t.Fatal(err)
	}
}
