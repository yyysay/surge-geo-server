package runtimecfg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestApplyPublishesOnlyValidCompleteSnapshot(t *testing.T) {
	server := runtimeSourceServer(t)
	defer server.Close()
	dataDir := t.TempDir()
	initial := Config{
		GeoSiteURL: server.URL + "/geosite.dat",
		GeoIPURL:   server.URL + "/geoip.dat",
		RegexMode:  "strict",
	}
	state, err := New(context.Background(), Options{DataDir: dataDir, Initial: initial})
	if err != nil {
		t.Fatal(err)
	}
	before := state.Snapshot()
	invalid := initial
	invalid.RegexMode = "invalid"
	if _, err := state.Apply(context.Background(), invalid); err == nil {
		t.Fatal("invalid runtime config was hot-applied")
	}
	if state.Snapshot() != before || state.Snapshot().Revision != 1 {
		t.Fatal("failed apply changed the active snapshot")
	}
	badSource := initial
	badSource.GeoSiteURL = server.URL + "/bad.dat"
	if _, err := state.Apply(context.Background(), badSource); err == nil {
		t.Fatal("malformed source was hot-applied")
	}
	if state.Snapshot() != before || state.Snapshot().Config.GeoSiteURL != initial.GeoSiteURL {
		t.Fatal("failed source apply changed the active snapshot")
	}

	updated := initial
	updated.RegexMode = "balanced"
	after, err := state.Apply(context.Background(), updated)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 2 {
		t.Fatalf("revision = %d, want 2", after.Revision)
	}
	if after.Config.RegexMode != "balanced" {
		t.Fatalf("regex mode = %q, want balanced", after.Config.RegexMode)
	}
}

func TestReloadConfigFile(t *testing.T) {
	server := runtimeSourceServer(t)
	defer server.Close()
	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "runtime.json")
	initial := Config{
		GeoSiteURL: server.URL + "/geosite.dat",
		GeoIPURL:   server.URL + "/geoip.dat",
		RegexMode:  "strict",
	}
	state, err := New(context.Background(), Options{DataDir: dataDir, ConfigFile: configPath, Initial: initial})
	if err != nil {
		t.Fatal(err)
	}
	updated := initial
	updated.RegexMode = "balanced"
	raw, _ := json.Marshal(updated)
	if err := os.WriteFile(configPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := state.ReloadConfig(context.Background())
	if err != nil || !changed {
		t.Fatalf("changed = %v, err = %v", changed, err)
	}
	if state.Snapshot().Config.RegexMode != "balanced" || state.Snapshot().Revision != 2 {
		t.Fatalf("unexpected snapshot: %#v", state.Snapshot())
	}
}

func TestNewUsesValidLocalDataWhenInitialSyncFails(t *testing.T) {
	server := runtimeSourceServer(t)
	geositeURL := server.URL + "/geosite.dat"
	geoipURL := server.URL + "/geoip.dat"
	server.Close()

	dataDir := t.TempDir()
	sourceDir := filepath.Join(dataDir, "sources")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "geosite.dat"), runtimeGeoSiteData(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "geoip.dat"), runtimeGeoIPData(), 0o644); err != nil {
		t.Fatal(err)
	}

	state, err := New(context.Background(), Options{DataDir: dataDir, Initial: Config{
		GeoSiteURL: geositeURL,
		GeoIPURL:   geoipURL,
		RegexMode:  "strict",
	}})
	if err != nil {
		t.Fatal(err)
	}
	status := state.Snapshot().Dataset.Status()
	if status.GeoSiteSets != 1 || status.GeoIPSets != 1 {
		t.Fatalf("unexpected local data status: %#v", status)
	}
}

func TestRefreshRejectsInvalidCandidateWithoutReplacingCachedData(t *testing.T) {
	geosite := runtimeGeoSiteData()
	geoip := runtimeGeoIPData()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geosite.dat":
			_, _ = w.Write(geosite)
		case "/geoip.dat":
			_, _ = w.Write(geoip)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dataDir := t.TempDir()
	state, err := New(context.Background(), Options{DataDir: dataDir, Initial: Config{
		GeoSiteURL: server.URL + "/geosite.dat",
		GeoIPURL:   server.URL + "/geoip.dat",
		RegexMode:  "strict",
	}})
	if err != nil {
		t.Fatal(err)
	}
	beforeSnapshot := state.Snapshot()
	geositePath := filepath.Join(dataDir, "sources", "geosite.dat")
	beforeDisk, err := os.ReadFile(geositePath)
	if err != nil {
		t.Fatal(err)
	}

	// This is non-empty and valid protobuf wire data, but contains no Geosite sets.
	geosite = []byte{0x10, 0x01}
	changed, err := state.Refresh(context.Background())
	if err == nil {
		t.Fatal("empty dataset candidate was accepted")
	}
	if changed {
		t.Fatal("invalid candidate was reported as published")
	}
	if state.Snapshot() != beforeSnapshot || state.Snapshot().Revision != 1 {
		t.Fatal("invalid candidate changed the active snapshot")
	}
	afterDisk, readErr := os.ReadFile(geositePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(afterDisk, beforeDisk) {
		t.Fatal("invalid candidate replaced the cached geosite DAT")
	}
}

func TestRefreshPersistsValidatorsAndKeepsUnchangedSnapshot(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	var notModified atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := version.Load()
		etag := fmt.Sprintf(`"v%d"`, n)
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.URL.Path == "/geosite.dat" {
			data := runtimeGeoSiteData()
			if n == 3 {
				data = []byte{0xff}
			}
			if n == 4 {
				data = bytes.ReplaceAll(data, []byte("exact"), []byte("other"))
			}
			_, _ = w.Write(data)
		} else {
			_, _ = w.Write(runtimeGeoIPData())
		}
	}))
	defer server.Close()
	state, err := New(context.Background(), Options{DataDir: t.TempDir(), Initial: Config{
		GeoSiteURL: server.URL + "/geosite.dat", GeoIPURL: server.URL + "/geoip.dat", RegexMode: "strict",
	}})
	if err != nil {
		t.Fatal(err)
	}
	before := state.Snapshot()
	info, err := os.Stat(state.geositePath)
	if err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	for range 2 {
		changed, err := state.Refresh(context.Background())
		if err != nil || changed || state.Snapshot() != before {
			t.Fatalf("unchanged refresh: changed = %v, err = %v", changed, err)
		}
	}
	if notModified.Load() != 2 {
		t.Fatal("new validators were not persisted for the next refresh")
	}
	afterInfo, err := os.Stat(state.geositePath)
	if err != nil || !os.SameFile(info, afterInfo) {
		t.Fatal("unchanged DAT was replaced")
	}
	beforeMeta, err := os.ReadFile(state.geositePath + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	version.Store(3)
	if changed, err := state.Refresh(context.Background()); err == nil || changed {
		t.Fatal("invalid refresh succeeded")
	}
	afterMeta, err := os.ReadFile(state.geositePath + ".meta.json")
	if err != nil || !bytes.Equal(beforeMeta, afterMeta) {
		t.Fatal("failed candidate changed active metadata through hard link")
	}
	version.Store(4)
	if changed, err := state.Refresh(context.Background()); err != nil || !changed {
		t.Fatalf("valid refresh: changed = %v, err = %v", changed, err)
	}
	if state.Snapshot().Revision != before.Revision+1 {
		t.Fatal("valid refresh did not publish one new revision")
	}
	output, err := state.Snapshot().Dataset.RenderGeoSite("cn")
	if err != nil || output.Body != "DOMAIN,other.example.cn\n" {
		t.Fatalf("updated output = %+v, err = %v", output, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(state.dataDir, ".source-refresh-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("candidate directories leaked: %v, err = %v", leftovers, err)
	}
}

func TestNewFallsBackWhenUpstreamDATIsInvalid(t *testing.T) {
	server := runtimeSourceServer(t)
	defer server.Close()
	options := Options{DataDir: t.TempDir(), Initial: Config{
		GeoSiteURL: server.URL + "/geosite.dat", GeoIPURL: server.URL + "/geoip.dat", RegexMode: "strict",
	}}
	state, err := New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	config := state.Snapshot().Config
	config.GeoSiteURL = server.URL + "/bad.dat"
	if _, err := state.persistConfig(config); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Snapshot().Dataset.Status().GeoSiteHash != state.Snapshot().Dataset.Status().GeoSiteHash {
		t.Fatal("invalid startup candidate replaced the last good data")
	}
}

func TestEquivalentConfigKeepsSnapshot(t *testing.T) {
	server := runtimeSourceServer(t)
	defer server.Close()
	state, err := New(context.Background(), Options{DataDir: t.TempDir(), Initial: Config{
		GeoSiteURL: server.URL + "/geosite.dat", GeoIPURL: server.URL + "/geoip.dat", RegexMode: "strict",
	}})
	if err != nil {
		t.Fatal(err)
	}
	before := state.Snapshot()
	if after, err := state.Apply(context.Background(), before.Config); err != nil || after != before {
		t.Fatalf("no-op apply rebuilt the snapshot: err = %v", err)
	}
	raw, err := json.Marshal(before.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.configFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := state.ReloadConfig(context.Background()); err != nil || changed || state.Snapshot() != before {
		t.Fatalf("formatting-only edit rebuilt the snapshot: changed = %v, err = %v", changed, err)
	}
}

func runtimeSourceServer(t *testing.T) *httptest.Server {
	t.Helper()
	geosite := runtimeGeoSiteData()
	geoip := runtimeGeoIPData()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/geosite.dat":
			_, _ = w.Write(geosite)
		case "/geoip.dat":
			_, _ = w.Write(geoip)
		case "/bad.dat":
			_, _ = w.Write([]byte{0xff})
		default:
			http.NotFound(w, r)
		}
	}))
}

func runtimeGeoSiteData() []byte {
	var domain []byte
	domain = protowire.AppendTag(domain, 1, protowire.VarintType)
	domain = protowire.AppendVarint(domain, 3)
	domain = protowire.AppendTag(domain, 2, protowire.BytesType)
	domain = protowire.AppendString(domain, "exact.example.cn")
	var site []byte
	site = protowire.AppendTag(site, 1, protowire.BytesType)
	site = protowire.AppendString(site, "CN")
	site = protowire.AppendTag(site, 2, protowire.BytesType)
	site = protowire.AppendBytes(site, domain)
	var data []byte
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, site)
}

func runtimeGeoIPData() []byte {
	var cidr []byte
	cidr = protowire.AppendTag(cidr, 1, protowire.BytesType)
	cidr = protowire.AppendBytes(cidr, []byte{10, 0, 0, 0})
	cidr = protowire.AppendTag(cidr, 2, protowire.VarintType)
	cidr = protowire.AppendVarint(cidr, 8)
	var set []byte
	set = protowire.AppendTag(set, 1, protowire.BytesType)
	set = protowire.AppendString(set, "CN")
	set = protowire.AppendTag(set, 2, protowire.BytesType)
	set = protowire.AppendBytes(set, cidr)
	var data []byte
	data = protowire.AppendTag(data, 1, protowire.BytesType)
	return protowire.AppendBytes(data, set)
}
