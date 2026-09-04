package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheAvailableRequiresMatchingMetadata(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "source.dat")
	if err := os.WriteFile(destination, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if CacheAvailable("https://example.com/source.dat", destination) {
		t.Fatal("cache without metadata was accepted")
	}
	if err := os.WriteFile(destination+".meta.json", []byte(`{"url":"https://example.com/source.dat"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !CacheAvailable("https://example.com/source.dat", destination) {
		t.Fatal("matching cache metadata was rejected")
	}
	if CacheAvailable("https://other.example/source.dat", destination) {
		t.Fatal("cache from a different URL was accepted")
	}
}

func TestSyncRejectsEmptyResponseWithoutReplacingDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "source.dat")
	original := []byte("valid cached data")
	if err := os.WriteFile(destination, original, 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := NewFetcher().Sync(context.Background(), server.URL, destination)
	if err == nil {
		t.Fatal("empty response was accepted")
	}
	if changed {
		t.Fatal("empty response was reported as a change")
	}
	after, readErr := os.ReadFile(destination)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(original) {
		t.Fatalf("destination was replaced: got %q, want %q", after, original)
	}
}
