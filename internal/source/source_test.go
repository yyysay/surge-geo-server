package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
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

func TestSyncTracksValidatorsForIdenticalContentAnd304(t *testing.T) {
	var step atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := step.Add(1)
		want := map[int32]string{1: "", 2: `"v1"`, 3: `"v2"`, 4: `W/"v3"`}[n]
		if r.Header.Get("If-None-Match") != want || r.Header.Get("If-Modified-Since") != "" {
			t.Errorf("request %d: validators = %v", n, r.Header)
		}
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		switch n {
		case 1, 2:
			w.Header().Set("ETag", fmt.Sprintf(`"v%d"`, n))
			_, _ = w.Write([]byte("same content"))
		case 3, 4:
			w.Header().Set("ETag", `W/"v3"`)
			w.WriteHeader(http.StatusNotModified)
		}
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "source.dat")
	fetcher := NewFetcher()
	for i := range 4 {
		changed, err := fetcher.Sync(context.Background(), server.URL, destination)
		if err != nil || changed != (i == 0) {
			t.Fatalf("sync %d: changed = %v, err = %v", i, changed, err)
		}
	}
	raw, err := os.ReadFile(destination + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	var meta metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ETag != `W/"v3"` || meta.SHA256 == "" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestSyncRepairsMissingOrEmptyCache(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%v", empty), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
					t.Error("sent validators without a usable cached body")
					w.WriteHeader(http.StatusNotModified)
					return
				}
				_, _ = w.Write([]byte("restored"))
			}))
			defer server.Close()
			destination := filepath.Join(t.TempDir(), "source.dat")
			if empty {
				if err := os.WriteFile(destination, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := saveMetadata(destination+".meta.json", metadata{}, metadata{URL: server.URL, ETag: `"old"`}); err != nil {
				t.Fatal(err)
			}
			changed, err := NewFetcher().Sync(context.Background(), server.URL, destination)
			if err != nil || !changed {
				t.Fatalf("changed = %v, err = %v", changed, err)
			}
			raw, err := os.ReadFile(destination)
			if err != nil || string(raw) != "restored" {
				t.Fatalf("data = %q, err = %v", raw, err)
			}
		})
	}
}

func TestSyncLastModifiedFallbackThroughRedirect(t *testing.T) {
	const modified = "Mon, 02 Jan 2006 15:04:05 GMT"
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", modified)
		if requests.Add(1) > 1 {
			if r.Header.Get("If-Modified-Since") != modified {
				t.Error("missing modification date on redirected request")
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte("data"))
	}))
	defer upstream.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, upstream.URL, http.StatusFound)
	}))
	defer redirect.Close()
	destination := filepath.Join(t.TempDir(), "source.dat")
	fetcher := NewFetcher()
	for i := range 2 {
		changed, err := fetcher.Sync(context.Background(), redirect.URL, destination)
		if err != nil || changed != (i == 0) {
			t.Fatalf("changed = %v, err = %v", changed, err)
		}
	}
}

func TestSyncRejectsBadResponsesWithoutChangingCache(t *testing.T) {
	for _, kind := range []string{"unsolicited-304", "truncated", "too-large", "http-error"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch kind {
				case "unsolicited-304":
					w.WriteHeader(http.StatusNotModified)
				case "truncated":
					w.Header().Set("Content-Length", "100")
					_, _ = w.Write([]byte("partial"))
				case "too-large":
					w.Header().Set("Content-Length", fmt.Sprint(maxDownloadSize+1))
				case "http-error":
					w.WriteHeader(http.StatusBadGateway)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			destination := filepath.Join(dir, "source.dat")
			if err := os.WriteFile(destination, []byte("original"), 0o644); err != nil {
				t.Fatal(err)
			}
			changed, err := NewFetcher().Sync(context.Background(), server.URL, destination)
			if err == nil || changed {
				t.Fatalf("changed = %v, err = %v", changed, err)
			}
			raw, err := os.ReadFile(destination)
			if err != nil || string(raw) != "original" {
				t.Fatalf("cache = %q, err = %v", raw, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary download leaked: %v, err = %v", entries, err)
			}
		})
	}
}

func TestSyncSourceChangeDropsOldValidators(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			t.Error("reused another URL's ETag")
		}
		_, _ = w.Write([]byte("new"))
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "source.dat")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveMetadata(destination+".meta.json", metadata{}, metadata{URL: server.URL + "/old", ETag: `"old"`}); err != nil {
		t.Fatal(err)
	}
	if changed, err := NewFetcher().Sync(context.Background(), server.URL, destination); err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
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
