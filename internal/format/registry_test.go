package format_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	formatmodule "github.com/mlops-ca/notarx/internal/format"
	"github.com/mlops-ca/notarx/internal/repository"
)

type module struct {
	name   repository.Format
	routes []formatmodule.Route
}

func (m *module) Format() repository.Format    { return m.name }
func (m *module) Routes() []formatmodule.Route { return m.routes }

type identityOnly struct{ name repository.Format }

func (m identityOnly) Format() repository.Format { return m.name }

func TestRegistryRejectsInvalidAndDuplicateModules(t *testing.T) {
	var absent *module
	for _, modules := range [][]formatmodule.Module{{nil}, {absent}, {&module{}}, {identityOnly{"raw"}, identityOnly{"raw"}}} {
		if _, err := formatmodule.New(modules...); err == nil {
			t.Fatalf("accepted invalid registration: %#v", modules)
		}
	}
}
func TestRegistryDispatchesOnlyRegisteredCapabilityAndReturnsIndependentOrder(t *testing.T) {
	called := 0
	registry, err := formatmodule.New(identityOnly{"zeta"}, &module{name: "raw", routes: []formatmodule.Route{{Pattern: "/raw/", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++; w.WriteHeader(http.StatusNoContent) })}}})
	if err != nil {
		t.Fatal(err)
	}
	got := registry.Formats()
	if !reflect.DeepEqual(got, []repository.Format{"raw", "zeta"}) {
		t.Fatalf("formats=%v", got)
	}
	got[0] = "modified"
	if registry.Formats()[0] != "raw" {
		t.Fatal("mutated registry formats")
	}
	if _, ok := formatmodule.Capability[formatmodule.NativeRoutes](registry, "zeta"); ok {
		t.Fatal("invented native capability")
	}
	if _, ok := formatmodule.Capability[formatmodule.NativeRoutes](registry, "missing"); ok {
		t.Fatal("found absent module")
	}
	mux := http.NewServeMux()
	if err := registry.Mount(mux); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/raw/example/file", nil))
	if response.Code != http.StatusNoContent || called != 1 {
		t.Fatalf("status=%d calls=%d", response.Code, called)
	}
}
func TestRegistryRejectsRoutesBeforeMountingAny(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	var nilHandler *http.ServeMux
	for _, routes := range [][]formatmodule.Route{
		{{Pattern: "/valid/", Handler: handler}, {Pattern: "/bad/", Handler: nilHandler}},
		{{Pattern: "/valid/", Handler: handler}, {Pattern: "/valid/", Handler: handler}},
		{{Pattern: "/valid/", Handler: handler}, {Pattern: "/bad/{", Handler: handler}},
		{{Pattern: "", Handler: handler}},
	} {
		registry, err := formatmodule.New(&module{name: "raw", routes: routes})
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		if err := registry.Mount(mux); err == nil {
			t.Fatalf("accepted invalid routes %v", routes)
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/valid/file", nil))
		if response.Code != http.StatusNotFound {
			t.Fatal("partially mounted invalid routes")
		}
	}
}
func TestRegistryZeroValueAndMissingTarget(t *testing.T) {
	var registry *formatmodule.Registry
	if _, ok := registry.Lookup("raw"); ok {
		t.Fatal("nil registry found module")
	}
	if len(registry.Formats()) != 0 {
		t.Fatal("nil registry has formats")
	}
	registry, err := formatmodule.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Mount(nil); err == nil {
		t.Fatal("nil mux accepted")
	}
}

type declaresEverything struct{ identityOnly }

func (declaresEverything) EnqueuePromotion(context.Context, string, string, string, string, string, ...repository.AuditRecord) (repository.LifecycleJob, bool, error) {
	return repository.LifecycleJob{}, false, nil
}
func (declaresEverything) RunPromotionJobs(context.Context, int) error { return nil }
func (declaresEverything) ExportUnit(context.Context, string, string, string) (formatmodule.ExportedUnit, error) {
	return formatmodule.ExportedUnit{}, nil
}
func (declaresEverything) ImportUnit(context.Context, string, formatmodule.ExportedUnit, func(formatmodule.BlobDescriptor) (io.ReadCloser, error)) error {
	return nil
}
func (declaresEverything) TagName(string) (string, bool) { return "", false }

func (declaresEverything) TagsOfName(context.Context, string, string) (formatmodule.TagLookup, error) {
	return nil, nil
}
func (declaresEverything) ReleaseMembers(context.Context, string, string, string) ([]formatmodule.ReleaseMember, error) {
	return nil, nil
}

func TestRegistryReportsTheOptionalCapabilitiesAModuleDeclares(t *testing.T) {
	registry, err := formatmodule.New(declaresEverything{identityOnly{"full"}}, identityOnly{"bare"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"promotion", "promotion-publisher", "exporter", "importer", "tagged", "release-set"}
	if got := registry.Capabilities("full"); !reflect.DeepEqual(got, want) {
		t.Fatalf("full declares %v, want %v", got, want)
	}
	if got := registry.Capabilities("bare"); len(got) != 0 {
		t.Fatalf("bare declares %v", got)
	}
	if got := registry.Capabilities("missing"); got != nil {
		t.Fatalf("missing declares %v", got)
	}
	var none *formatmodule.Registry
	if none.Capabilities("full") != nil {
		t.Fatal("a nil registry declares capabilities")
	}
	if _, ok := formatmodule.Capability[formatmodule.PromotionPublisher](registry, "bare"); ok {
		t.Fatal("invented promotion publisher")
	}
}

func TestPromotionOptionsTravelInTheContext(t *testing.T) {
	if got := formatmodule.PromotionOptionsFrom(context.Background()); got != (formatmodule.PromotionOptions{}) {
		t.Fatalf("empty context carries %+v", got)
	}
	options := formatmodule.PromotionOptions{Actor: "alice", APTTargetSuite: "stable"}
	if got := formatmodule.PromotionOptionsFrom(formatmodule.WithPromotionOptions(context.Background(), options)); got != options {
		t.Fatalf("options=%+v", got)
	}
}
