package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yyysay/surge-geo-server/internal/model"
	"github.com/yyysay/surge-geo-server/internal/rules"
	"github.com/yyysay/surge-geo-server/internal/runtimecfg"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestConfigEndpoint(t *testing.T) {
	want := runtimecfg.Config{
		GeoSiteURL: "https://example.com/geosite.dat",
		GeoIPURL:   "https://example.com/geoip.dat",
		RegexMode:  "balanced",
	}
	runtime := testRuntime(t, want)
	handler := New(runtime, "")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got configResponse
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.GeoSiteURL != want.GeoSiteURL || got.GeoIPURL != want.GeoIPURL || got.RegexMode != want.RegexMode {
		t.Fatalf("config = %#v, want %#v", got.Config, want)
	}
}

func TestHotReloadEndpointRequiresAuthorizationAndApplies(t *testing.T) {
	config := runtimecfg.Config{
		GeoSiteURL: "https://example.com/geosite.dat",
		GeoIPURL:   "https://example.com/geoip.dat",
		RegexMode:  "strict",
	}
	runtime := testRuntime(t, config)
	handler := New(runtime, "secret")
	body, _ := json.Marshal(runtimecfg.Config{
		GeoSiteURL: config.GeoSiteURL,
		GeoIPURL:   config.GeoIPURL,
		RegexMode:  "balanced",
	})
	unauthorized := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, unauthorized)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unauthorized status = %d", recorder.Code)
	}
	authorized := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	authorized.Header.Set("Authorization", "Bearer secret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorized)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if runtime.Snapshot().Config.RegexMode != "balanced" || runtime.Snapshot().Revision != 2 {
		t.Fatalf("runtime was not updated: %#v", runtime.Snapshot())
	}
}

func TestHotReloadConfigRejectsRules(t *testing.T) {
	config := runtimecfg.Config{
		GeoSiteURL: "https://example.com/geosite.dat",
		GeoIPURL:   "https://example.com/geoip.dat",
		RegexMode:  "strict",
	}
	handler := New(testRuntime(t, config), "secret")
	body := []byte(`{"geosite_url":"https://example.com/geosite.dat","geoip_url":"https://example.com/geoip.dat","regex_mode":"strict","rules":["GEOSITE,cn,DIRECT"]}`)
	request := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestLoopbackAuthorizationRejectsProxyAndPublicHost(t *testing.T) {
	request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/api/config", nil)
	request.RemoteAddr = "127.0.0.1:43210"
	if !authorized(request, "") {
		t.Fatal("direct loopback request was rejected")
	}
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	if authorized(request, "") {
		t.Fatal("proxied request without token was accepted")
	}
	request.Header.Del("X-Forwarded-For")
	request.Host = "rules.example.com"
	if authorized(request, "") {
		t.Fatal("public Host request without token was accepted")
	}
}

func TestUIQueryReturnsFirstPolicyAndRulesAPIIsAbsent(t *testing.T) {
	config := runtimecfg.Config{
		GeoSiteURL: "https://example.com/geosite.dat",
		GeoIPURL:   "https://example.com/geoip.dat",
		RegexMode:  "strict",
	}
	handler := New(testRuntime(t, config), "")
	recorder := httptest.NewRecorder()
	form := url.Values{"value": {"www.example.com"}, "rules": {"GEOSITE,example,Direct"}}
	request := httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var got struct {
		Policy string `json:"policy"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Policy != "Direct" {
		t.Fatalf("policy = %q, want Direct", got.Policy)
	}
	recorder = httptest.NewRecorder()
	form.Set("rules", "MATCH,Proxy")
	request = httptest.NewRequest(http.MethodPost, "/query", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("MATCH query status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	for _, path := range []string{"/api/rules", "/api/rules/evaluate?value=www.example.com"} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want %d", path, recorder.Code, http.StatusNotFound)
		}
	}
}

func TestRawGeoSiteEndpoint(t *testing.T) {
	dataset, err := rules.New(testGeoSiteData(), nil, rules.RegexStrict)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{snapshot: &runtimecfg.Snapshot{Dataset: dataset, Config: runtimecfg.Config{RegexMode: "strict"}, Revision: 1, LoadedAt: time.Now()}}
	handler := New(runtime, "")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/geosite/example", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var site model.GeoSite
	if err := json.NewDecoder(recorder.Body).Decode(&site); err != nil {
		t.Fatal(err)
	}
	if site.Name != "example" || len(site.Rules) != 1 || site.Rules[0].Kind != model.DomainSuffix || site.Rules[0].Value != "example.com" {
		t.Fatalf("site = %#v", site)
	}
}

type fakeRuntime struct {
	snapshot *runtimecfg.Snapshot
	applyErr error
}

func (f *fakeRuntime) Snapshot() *runtimecfg.Snapshot { return f.snapshot }

func (f *fakeRuntime) Apply(_ context.Context, config runtimecfg.Config) (*runtimecfg.Snapshot, error) {
	if f.applyErr != nil {
		return nil, f.applyErr
	}
	f.snapshot = &runtimecfg.Snapshot{
		Dataset:  f.snapshot.Dataset,
		Config:   config,
		Revision: f.snapshot.Revision + 1,
		LoadedAt: time.Now(),
	}
	return f.snapshot, nil
}

func testRuntime(t *testing.T, config runtimecfg.Config) *fakeRuntime {
	t.Helper()
	dataset, err := rules.New(testGeoSiteData(), nil, rules.RegexStrict)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeRuntime{snapshot: &runtimecfg.Snapshot{
		Dataset: dataset, Config: config, Revision: 1, LoadedAt: time.Now(),
	}}
}

func testGeoSiteData() []byte {
	var domain []byte
	domain = protowire.AppendTag(domain, 1, protowire.VarintType)
	domain = protowire.AppendVarint(domain, 2)
	domain = protowire.AppendTag(domain, 2, protowire.BytesType)
	domain = protowire.AppendString(domain, "example.com")

	var site []byte
	site = protowire.AppendTag(site, 1, protowire.BytesType)
	site = protowire.AppendString(site, "EXAMPLE")
	site = protowire.AppendTag(site, 2, protowire.BytesType)
	site = protowire.AppendBytes(site, domain)

	var result []byte
	result = protowire.AppendTag(result, 1, protowire.BytesType)
	return protowire.AppendBytes(result, site)
}
