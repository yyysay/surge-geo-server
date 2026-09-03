package runtimecfg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		GeoSiteURL: "http://127.0.0.1:1/geosite.dat",
		GeoIPURL:   "http://127.0.0.1:1/geoip.dat",
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
