package source

import (
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
