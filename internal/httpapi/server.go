package httpapi

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/yangtudou/surge-geo-server/internal/rules"
)

//go:embed index.html
var assets embed.FS

type CurrentDataset func() *rules.Dataset

type SourceConfig struct {
	GeoSiteURL string `json:"geosite_url"`
	GeoIPURL   string `json:"geoip_url"`
}

func New(current CurrentDataset, sources SourceConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", serveIndex)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, current().Status())
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, sources)
	})
	mux.HandleFunc("GET /api/lookup/domain", func(w http.ResponseWriter, r *http.Request) {
		value := r.URL.Query().Get("value")
		matches, err := current().LookupDomain(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": value, "matches": matches})
	})
	mux.HandleFunc("GET /api/lookup/ip", func(w http.ResponseWriter, r *http.Request) {
		value := r.URL.Query().Get("value")
		matches, err := current().LookupIP(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": value, "matches": matches})
	})
	mux.HandleFunc("GET /geosite", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, current().GeoSiteIndex())
	})
	mux.HandleFunc("GET /geosite/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, skipped, err := current().RenderGeoSite(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		w.Header().Set("X-Surge-Geo-Skipped-Regex", fmt.Sprintf("%d", skipped))
		writeRuleSet(w, r, body)
	})
	mux.HandleFunc("GET /geoip", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, current().GeoIPIndex())
	})
	mux.HandleFunc("GET /geoip/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, err := current().RenderGeoIP(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeRuleSet(w, r, body)
	})
	return securityHeaders(mux)
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func writeRuleSet(w http.ResponseWriter, r *http.Request, body []byte) {
	hash := sha256.Sum256(body)
	etag := fmt.Sprintf("\"%x\"", hash[:12])
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600, stale-while-revalidate=86400")
	w.Header().Set("ETag", etag)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"style-src 'self' 'unsafe-inline'",
			"script-src 'self' 'unsafe-inline'",
		}, "; "))
		next.ServeHTTP(w, r)
	})
}
