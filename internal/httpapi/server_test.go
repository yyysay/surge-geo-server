package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigEndpoint(t *testing.T) {
	want := SourceConfig{
		GeoSiteURL: "https://example.com/geosite.dat",
		GeoIPURL:   "https://example.com/geoip.dat",
	}
	handler := New(nil, want)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var got SourceConfig
	if err := json.NewDecoder(recorder.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
}
