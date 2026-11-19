package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlops-ca/notarx/internal/repository"
)

func TestRepositoryArtifactUsagePaginationAndSearch(t *testing.T) {
	ctx := context.Background()
	store := repository.NewMemoryStore()
	repo, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "usage-pages", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateHostedRepository(ctx, repository.HostedRepository{ID: uuid.NewString(), Name: "usage-private", Format: repository.FormatRaw})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReplaceRepositoryGrants(ctx, repo.ID, []repository.RepositoryGrant{{Principal: "usage-reader", Scopes: []string{"repositories:read"}}}, "1"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		if err := store.RecordArtifactUsage(ctx, repository.ArtifactUsageStat{Repository: repo.Name, Format: "raw", Resource: fmt.Sprintf("a/%03d", i), TotalBytes: 10, LastDownloadedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, stat := range []repository.ArtifactUsageStat{
		{Repository: repo.Name, Format: "raw", Resource: "literal%_\\.txt", TotalBytes: 10, LastDownloadedAt: time.Now()},
		{Repository: other.Name, Format: "raw", Resource: "private-only", TotalBytes: 999, LastDownloadedAt: time.Now()},
	} {
		if err := store.RecordArtifactUsage(ctx, stat); err != nil {
			t.Fatal(err)
		}
	}
	auth := testAuthenticator()
	auth.LegacyReadPermissive = false
	handler := newTestGateway(t, GatewayOptions{Dependencies: Dependencies{}, Store: store, Adapter: TestAdapter{}, Authenticator: auth})
	request := func(id, query string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v2/repositories/"+id+"/artifact-usage"+query, nil)
		if authenticated {
			authorize(r, auth.IssueToken("usage-reader"))
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	// Walk the whole listing with page tokens: 205 addresses in pages of 100.
	var token, firstToken string
	var walked []string
	for page := 0; page < 3; page++ {
		query := "?limit=100&q=a%2F"
		if token != "" {
			query += "&pageToken=" + url.QueryEscape(token)
		}
		w := request(repo.ID, query, true)
		var result struct {
			Total         struct {
				Value    int64  `json:"value"`
				Relation string `json:"relation"`
			} `json:"total"`
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Resource string `json:"resource"`
			} `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Total.Value != 205 || result.Total.Relation != "eq" {
			t.Fatalf("page %d status=%d body=%s", page, w.Code, w.Body.String())
		}
		for _, item := range result.Items {
			walked = append(walked, item.Resource)
		}
		token = result.NextPageToken
		if page == 0 {
			firstToken = token
		}
		if (page < 2) != (token != "") {
			t.Fatalf("page %d nextPageToken=%q", page, token)
		}
	}
	if len(walked) != 205 || walked[0] != "a/000" || walked[204] != "a/204" {
		t.Fatalf("walked %d addresses: first=%s", len(walked), walked[0])
	}
	if w := request(repo.ID, "?limit=1&q=missing&pageToken="+url.QueryEscape(firstToken), true); w.Code != 400 {
		t.Errorf("a token for another query: status=%d", w.Code)
	}
	for _, tc := range []struct {
		query string
		count int64
		paths []string
	}{
		{"?limit=2&q=" + url.QueryEscape("%_\\"), 1, []string{"literal%_\\.txt"}},
		{"?q=missing", 0, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			w := request(repo.ID, tc.query, true)
			var result struct {
				Total struct {
					Value int64 `json:"value"`
				} `json:"total"`
				Totals repository.ArtifactUsageTotals `json:"totals"`
				Items      []struct {
					Resource string `json:"resource"`
				} `json:"items"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if result.Total.Value != tc.count || len(result.Items) != len(tc.paths) {
				t.Fatalf("count=%d items=%v", result.Total.Value, result.Items)
			}
			for i, path := range tc.paths {
				if result.Items[i].Resource != path {
					t.Fatalf("item=%s want=%s", result.Items[i].Resource, path)
				}
			}
			if result.Totals.Resources != 206 || result.Totals.DownloadCount != 206 || result.Totals.TotalBytes != 2060 {
				t.Fatalf("whole-repository totals=%+v", result.Totals)
			}
		})
	}
	for _, query := range []string{"?limit=0", "?limit=501", "?pageToken=not-a-token", "?q=" + strings.Repeat("a", 513), "?q=%00", "?q=%FF"} {
		if w := request(repo.ID, query, true); w.Code != 400 {
			t.Errorf("%s: status=%d", query, w.Code)
		}
	}
	if w := request(repo.ID, "?q=a", false); w.Code != 401 {
		t.Errorf("anonymous=%d", w.Code)
	}
	if w := request(other.ID, "?q=private", true); w.Code != 403 {
		t.Errorf("other repository=%d", w.Code)
	}
}
