package remoteinstance

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/egress"
)

func validDocument() Document {
	return Document{InstanceID: uuid.NewString(), APIVersion: APIVersion, Formats: []string{"raw"}, Limits: DefaultLimits()}
}

func TestNormalizeBaseURL(t *testing.T) {
	for raw, want := range map[string]string{
		"https://Peer.Example.test":           "https://peer.example.test",
		"https://peer.example.test/":          "https://peer.example.test",
		" https://peer.example.test:8443/a/ ": "https://peer.example.test:8443/a",
	} {
		if got, err := NormalizeBaseURL(raw); err != nil || got != want {
			t.Errorf("%q = %q err=%v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "http://peer.example.test", "ftp://peer.example.test", "https://", "https://user:pass@peer.example.test",
		"https://peer.example.test?x=1", "https://peer.example.test#frag", "peer.example.test", "https://peer.example.test:99999x",
		"https://" + strings.Repeat("a", MaxBaseURLLength),
	} {
		if got, err := NormalizeBaseURL(raw); err == nil {
			t.Errorf("%q accepted as %q", raw, got)
		}
	}
}

func TestDocumentValidate(t *testing.T) {
	document := validDocument()
	document.InstanceID = strings.ToUpper(document.InstanceID)
	got, err := document.Validate()
	if err != nil || got.InstanceID != strings.ToLower(document.InstanceID) {
		t.Fatalf("valid document = %#v err=%v", got, err)
	}
	for name, mutate := range map[string]func(*Document){
		"instance id":    func(d *Document) { d.InstanceID = "not-a-uuid" },
		"api version":    func(d *Document) { d.APIVersion = "2" },
		"empty format":   func(d *Document) { d.Formats = []string{""} },
		"repeat format":  func(d *Document) { d.Formats = []string{"raw", "raw"} },
		"long format":    func(d *Document) { d.Formats = []string{strings.Repeat("f", 33)} },
		"blob limit":     func(d *Document) { d.Limits.MaxBlobBytes = 0 },
		"session limit":  func(d *Document) { d.Limits.MaxSessionBytes = -1 },
		"blobs limit":    func(d *Document) { d.Limits.MaxBlobsPerSession = 0 },
		"sessions limit": func(d *Document) { d.Limits.MaxOpenSessionsPerPrincipal = 0 },
	} {
		candidate := validDocument()
		mutate(&candidate)
		if _, err := candidate.Validate(); !errors.Is(err, ErrInvalidDocument) {
			t.Errorf("%s: err = %v, want ErrInvalidDocument", name, err)
		}
	}
	empty := validDocument()
	empty.Formats = nil
	if got, err := empty.Validate(); err != nil || got.Formats == nil {
		t.Fatalf("nil formats = %#v err=%v", got.Formats, err)
	}
}

func newDiscoverer(t *testing.T, handler http.Handler) (Discoverer, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	return NewDiscoverer(Options{AllowPrivateNetworks: true, RootCAs: roots}), server.URL
}

func TestDiscoverReadsTheInstanceDocumentAnonymously(t *testing.T) {
	document := validDocument()
	var sawCredential atomic.Bool
	discoverer, base := newDiscoverer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != WellKnownPath || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			sawCredential.Store(true)
		}
		_ = json.NewEncoder(w).Encode(document)
	}))
	got, err := discoverer.Discover(context.Background(), base+"/")
	if err != nil || got.InstanceID != document.InstanceID || got.Limits != document.Limits || len(got.Formats) != 1 {
		t.Fatalf("document = %#v err=%v", got, err)
	}
	if sawCredential.Load() {
		t.Fatal("the well-known request carried a credential")
	}
}

func TestDiscoverRejectsBadAnswers(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusServiceUnavailable) },
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://elsewhere.example.test/", http.StatusFound)
		},
		"json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"version": func(w http.ResponseWriter, _ *http.Request) {
			document := validDocument()
			document.APIVersion = "9"
			_ = json.NewEncoder(w).Encode(document)
		},
		"oversize": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"instanceId":"` + strings.Repeat("x", MaxDocumentBytes) + `"}`))
		},
	} {
		discoverer, base := newDiscoverer(t, handler)
		if _, err := discoverer.Discover(context.Background(), base); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name == "redirect" && !errors.Is(err, egress.ErrPeerRedirect) {
			t.Errorf("redirect err = %v", err)
		}
	}
}

func TestDiscoverRefusesPrivateNetworksByDefault(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(validDocument())
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	_, err := NewDiscoverer(Options{RootCAs: roots}).Discover(context.Background(), server.URL)
	if !errors.Is(err, egress.ErrPeerAddressBlocked) {
		t.Fatalf("err = %v, want ErrPeerAddressBlocked", err)
	}
	if _, err = NewDiscoverer(Options{}).Discover(context.Background(), "http://peer.example.test"); err == nil {
		t.Fatal("plain HTTP accepted")
	}
}

func TestTokenPurposeBindsTheInstanceID(t *testing.T) {
	if TokenPurpose("a") == TokenPurpose("b") || TokenPurpose("a") != "remote-instance:a" {
		t.Fatal("the purpose must be bound to the instance id")
	}
}
