//go:build integration

package objectstore

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestS3StoreConcurrentBucketInitializationIsIdempotent(t *testing.T) {
	endpoint := os.Getenv("TEST_OBJECT_STORE_ENDPOINT")
	accessKey := os.Getenv("TEST_OBJECT_STORE_ACCESS_KEY")
	secretKey := os.Getenv("TEST_OBJECT_STORE_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("TEST_OBJECT_STORE_ENDPOINT and credentials are required")
	}
	bucket := "gateway-concurrent-" + uuid.NewString()
	const clients = 8
	errorsByClient := make([]error, clients)
	var ready sync.WaitGroup
	var start sync.WaitGroup
	ready.Add(clients)
	start.Add(1)
	var workers sync.WaitGroup
	workers.Add(clients)
	for index := range clients {
		go func() {
			defer workers.Done()
			store, err := NewS3StoreWithCredentials(endpoint, accessKey, secretKey, bucket)
			if err != nil {
				errorsByClient[index] = err
				ready.Done()
				return
			}
			ready.Done()
			start.Wait()
			errorsByClient[index] = store.EnsureBucket(context.Background())
		}()
	}
	ready.Wait()
	start.Done()
	workers.Wait()
	for index, err := range errorsByClient {
		if err != nil {
			t.Fatalf("client %d failed concurrent bucket initialization: %v", index, err)
		}
	}
}
