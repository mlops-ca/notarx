package jose

import (
	"context"
	"testing"
)

func TestProbeReportsKeysAndAlgorithms(t *testing.T) {
	server := newIssuerServer(t, pubKey(t, RS256, "rsa-1"), pubKey(t, ES256, "ec-1"))
	got, err := Probe(context.Background(), server.issuer(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.JWKSURI != server.URL+"/jwks" || len(got.Keys) != 2 || got.Keys[0].KeyID != "rsa-1" || got.Keys[0].KeyType != "RSA" || got.Keys[1].Algorithm != ES256 {
		t.Fatalf("probe = %+v", got)
	}
	if len(got.Algorithms) != 2 || got.Algorithms[0] != ES256 || got.Algorithms[1] != RS256 {
		t.Fatalf("algorithms = %v", got.Algorithms)
	}
}

func TestProbeReadsAStaticKeySetAndRefusesBlockedAddresses(t *testing.T) {
	doc, err := MarshalJWKS(pubKey(t, EdDSA, "ed-1"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Probe(context.Background(), Issuer{Issuer: "https://k8s.example.test", Mode: ModeStatic, StaticJWKS: doc}, nil)
	if err != nil || got.JWKSURI != "" || len(got.Keys) != 1 || got.Keys[0].Algorithm != EdDSA || got.Keys[0].KeyType != "OKP" {
		t.Fatalf("static probe = %+v, %v", got, err)
	}
	server := newIssuerServer(t, pubKey(t, RS256, "rsa-1"))
	private := server.issuer()
	private.AllowPrivateNetwork = false
	if _, err := Probe(context.Background(), private, nil); err == nil {
		t.Fatal("a loopback issuer was probed without allow_private_network")
	}
	if _, err := Probe(context.Background(), Issuer{Issuer: "https://k8s.example.test", Mode: ModeStatic, StaticJWKS: []byte("{}")}, nil); err == nil {
		t.Fatal("an empty static key set was accepted")
	}
	server.fail.Store(true)
	if _, err := Probe(context.Background(), server.issuer(), nil); err == nil {
		t.Fatal("a failing JWKS endpoint was reported reachable")
	}
}
