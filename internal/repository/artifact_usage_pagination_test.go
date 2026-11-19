package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type usagePageStore interface {
	ArtifactUsageStore
	RecordArtifactUsage(context.Context, ArtifactUsageStat) error
}

func TestMemoryArtifactUsagePages(t *testing.T) {
	exerciseArtifactUsagePages(t, NewMemoryStore(), "usage-pages")
}

func exerciseArtifactUsagePages(t *testing.T, store usagePageStore, repo string) {
	t.Helper()
	ctx := context.Background()
	add := func(format, path string) {
		t.Helper()
		if err := store.RecordArtifactUsage(ctx, ArtifactUsageStat{Repository: repo, Format: format, Resource: path, TotalBytes: 1, LastDownloadedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 204; i >= 0; i-- {
		add("raw", fmt.Sprintf("a/%03d", i))
	}
	add("raw", "same")
	add("npm", "same")
	add("raw", "literal%_\\")
	add("raw", "中文")
	if err := store.RecordArtifactUsage(ctx, ArtifactUsageStat{Repository: repo + "-other", Format: "raw", Resource: "a/000"}); err != nil {
		t.Fatal(err)
	}
	// Walk the keyset pages: 205 "a/" addresses in 100-row pages, each continued from the previous token.
	page, err := store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 100, Query: "a/"})
	if err != nil || len(page.Items) != 100 || page.NextPageToken == "" || page.Items[0].Resource != "a/000" || page.Total != (ListTotal{Value: 205, Relation: TotalExact}) || page.Totals.Resources != 209 {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	page, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 100, Query: "a/", PageToken: page.NextPageToken})
	if err != nil || len(page.Items) != 100 || page.Items[0].Resource != "a/100" || page.NextPageToken == "" {
		t.Fatalf("second page=%+v err=%v", page, err)
	}
	add("raw", "a/204") // A download increment must not move an existing address.
	page, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 100, Query: "a/", PageToken: page.NextPageToken})
	if err != nil || len(page.Items) != 5 || page.Items[4].Resource != "a/204" || page.Items[4].DownloadCount != 2 || page.NextPageToken != "" {
		t.Fatalf("last page=%+v err=%v", page, err)
	}
	// The (resource, format) pair is the key: two formats of one resource straddle a page boundary.
	page, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 1, Query: "same"})
	if err != nil || len(page.Items) != 1 || page.Items[0].Format != "npm" || page.NextPageToken == "" || page.Total.Value != 2 {
		t.Fatalf("tie page=%+v err=%v", page, err)
	}
	page, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 1, Query: "same", PageToken: page.NextPageToken})
	if err != nil || len(page.Items) != 1 || page.Items[0].Format != "raw" || page.NextPageToken != "" {
		t.Fatalf("tie page 2=%+v err=%v", page, err)
	}
	page, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 500, Query: "%_\\"})
	if err != nil || len(page.Items) != 1 || page.Total.Value != 1 {
		t.Fatalf("literal page=%+v err=%v", page, err)
	}
	page, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 20, Query: "missing"})
	if err != nil || len(page.Items) != 0 || page.Total.Value != 0 || page.Totals.Resources != 209 {
		t.Fatalf("empty=%+v err=%v", page, err)
	}
	// A token is bound to the query that issued it.
	if first, err := store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 1, Query: "a/"}); err != nil {
		t.Fatal(err)
	} else if _, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 1, Query: "same", PageToken: first.NextPageToken}); !errors.Is(err, ErrInvalidPageToken) {
		t.Errorf("a token for another filter was accepted: %v", err)
	}
	if _, err = store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 1, PageToken: "not-a-token"}); !errors.Is(err, ErrInvalidPageToken) {
		t.Errorf("a malformed token was accepted: %v", err)
	}
	for _, query := range []ArtifactUsageQuery{{Limit: 0}, {Limit: 501}, {Limit: 1, Query: "\x00"}} {
		if _, err := store.QueryArtifactUsage(ctx, repo, query); err == nil {
			t.Errorf("accepted invalid query=%+v", query)
		}
	}
	if _, err := store.QueryArtifactUsage(ctx, "", ArtifactUsageQuery{Limit: 20}); err == nil {
		t.Error("empty repository must not query all repositories")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.QueryArtifactUsage(cancelled, repo, ArtifactUsageQuery{Limit: 20}); err == nil {
		t.Error("cancelled query succeeded")
	}
	// Each response must remain internally consistent while downloads accumulate.
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; i < 50; i++ {
			if err := store.RecordArtifactUsage(ctx, ArtifactUsageStat{Repository: repo, Format: "raw", Resource: "a/204", TotalBytes: 1, LastDownloadedAt: time.Now()}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	defer writers.Wait()
	for i := 0; i < 20; i++ {
		page, err := store.QueryArtifactUsage(ctx, repo, ArtifactUsageQuery{Limit: 500})
		if err != nil {
			t.Fatal(err)
		}
		var downloads, bytes int64
		for _, item := range page.Items {
			downloads += item.DownloadCount
			bytes += item.TotalBytes
		}
		if len(page.Items) != 209 || page.Total.Value != 209 || downloads != page.Totals.DownloadCount || bytes != page.Totals.TotalBytes {
			t.Fatalf("inconsistent snapshot: count=%d totals=%+v row sums=%d/%d", page.Total.Value, page.Totals, downloads, bytes)
		}
	}
}
