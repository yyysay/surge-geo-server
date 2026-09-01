package source

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
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

func (f *Fetcher) Sync(ctx context.Context, url, destination string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return false, err
	}
	metaPath := destination + ".meta.json"
	var old metadata
	if raw, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(raw, &old)
	}
	if old.URL != url {
		old = metadata{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	if old.ETag != "" {
		req.Header.Set("If-None-Match", old.ETag)
	}
	if old.LastModified != "" {
		req.Header.Set("If-Modified-Since", old.LastModified)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize+1))
	if err != nil {
		return false, err
	}
	if len(raw) > maxDownloadSize {
		return false, fmt.Errorf("GET %s: response exceeds %d bytes", url, maxDownloadSize)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if hash == old.SHA256 {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".download-*")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return false, err
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
	next := metadata{URL: url, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), SHA256: hash}
	metaRaw, _ := json.MarshalIndent(next, "", "  ")
	if err = os.WriteFile(metaPath, metaRaw, 0o644); err != nil {
		return true, err
	}
	return true, nil
}
