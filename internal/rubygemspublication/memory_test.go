package rubygemspublication_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
	d "github.com/mlops-ca/notarx/internal/rubygemspublication"
	f "github.com/mlops-ca/notarx/internal/testsupport/rubygemsfixture"
)

func TestRubyGemsMemoryPublicationStoreContract(t *testing.T) {
	repos := repository.NewMemoryStore()
	repo, e := repos.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "ruby-memory-" + uuid.NewString(), Format: repository.FormatRubyGems, Type: repository.RepositoryTypeHosted})
	if e != nil {
		t.Fatal(e)
	}
	f.StoreContract(t, d.NewMemory(repos, repos), repo)
}

func TestRubyGemsMemoryGovernanceStoreContract(t *testing.T) {
	repos := repository.NewMemoryStore()
	repo, err := repos.CreateHostedRepository(t.Context(), repository.HostedRepository{ID: uuid.NewString(), Name: "ruby-governance-" + uuid.NewString(), Format: repository.FormatRubyGems, Type: repository.RepositoryTypeHosted})
	if err != nil {
		t.Fatal(err)
	}
	f.GovernanceContract(t, d.NewMemory(repos, repos), repo)
}
