package source

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxDownloadSize = 256 << 20

type metadata struct {
	URL          string `json:"url,omitempty"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
}

type Fetcher struct {
	client *http.Client
}

func NewFetcher() *Fetcher {
	return &Fetcher{client: &http.Client{Timeout: 2 * time.Minute}}
}

// CacheAvailable reports whether destination and its metadata belong to sourceURL.
func CacheAvailable(sourceURL, destination string) bool {
	if _, err := os.Stat(destination); err != nil {
		return false
	}
	raw, err := os.ReadFile(destination + ".meta.json")
	if err != nil {
		return false
	}
	var cached metadata
	return json.Unmarshal(raw, &cached) == nil && cached.URL == sourceURL
}

func (f *Fetcher) Sync(ctx context.Context, url, destination string) (bool, error) {
	started := time.Now()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return false, err
	}
	metaPath := destination + ".meta.json"
	var old metadata
	if raw, err := os.ReadFile(metaPath); err == nil {
		if err := json.Unmarshal(raw, &old); err != nil {
			old = metadata{}
		}
	}
	info, statErr := os.Stat(destination)
	if old.URL != url || statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		old = metadata{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	if old.ETag != "" {
		req.Header.Set("If-None-Match", old.ETag)
	} else if old.LastModified != "" {
		req.Header.Set("If-Modified-Since", old.LastModified)
	}
	slog.DebugContext(ctx, "source request", "file", filepath.Base(destination), "conditional", old.ETag != "" || old.LastModified != "")
	resp, err := f.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	slog.DebugContext(ctx, "source response", "file", filepath.Base(destination), "status", resp.StatusCode, "etag", resp.Header.Get("ETag"), "last_modified", resp.Header.Get("Last-Modified"))
	if resp.StatusCode == http.StatusNotModified {
		if req.Header.Get("If-None-Match") == "" && req.Header.Get("If-Modified-Since") == "" {
			return false, fmt.Errorf("GET %s: unexpected 304 without a usable conditional cache", url)
		}
		next := old
		if value := resp.Header.Get("ETag"); value != "" {
			next.ETag = value
		}
		if value := resp.Header.Get("Last-Modified"); value != "" {
			next.LastModified = value
		}
		return false, saveMetadata(metaPath, old, next)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	if resp.ContentLength > maxDownloadSize {
		return false, fmt.Errorf("GET %s: response exceeds %d bytes", url, maxDownloadSize)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".download-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, maxDownloadSize+1))
	if err != nil {
		return false, err
	}
	if size == 0 {
		return false, fmt.Errorf("GET %s: empty response", url)
	}
	if size > maxDownloadSize {
		return false, fmt.Errorf("GET %s: response exceeds %d bytes", url, maxDownloadSize)
	}
	hash := fmt.Sprintf("%x", hasher.Sum(nil))
	next := metadata{URL: url, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), SHA256: hash}
	if hash == old.SHA256 {
		slog.DebugContext(ctx, "source content unchanged", "file", filepath.Base(destination), "bytes", size, "duration", time.Since(started))
		// Validators can change even when a release contains identical bytes.
		return false, saveMetadata(metaPath, old, next)
	}
	if err = tmp.Close(); err != nil {
		return false, err
	}
	if err = os.Chmod(tmpName, 0o644); err != nil {
		return false, err
	}
	if err = os.Rename(tmpName, destination); err != nil {
		return false, err
	}
	slog.DebugContext(ctx, "source candidate downloaded", "file", filepath.Base(destination), "bytes", size, "duration", time.Since(started))
	return true, saveMetadata(metaPath, old, next)
}

// Metadata is replaced atomically too: staged files may be hard links to the
// active cache, and a failed candidate must never modify that cache in place.
func saveMetadata(path string, old, next metadata) error {
	if old == next {
		return nil
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".metadata-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
