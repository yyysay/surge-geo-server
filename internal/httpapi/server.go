package httpapi

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/yyysay/surge-geo-server/internal/routing"
	"github.com/yyysay/surge-geo-server/internal/rules"
	"github.com/yyysay/surge-geo-server/internal/runtimecfg"
)

//go:embed index.html
var indexHTML string

type Runtime interface {
	Snapshot() *runtimecfg.Snapshot
	Apply(context.Context, runtimecfg.Config) (*runtimecfg.Snapshot, error)
}

type configResponse struct {
	runtimecfg.Config
	Revision uint64 `json:"revision"`
	LoadedAt string `json:"loaded_at"`
}

type statusResponse struct {
	rules.Status
	RuntimeRevision uint64 `json:"runtime_revision"`
	RuntimeLoadedAt string `json:"runtime_loaded_at"`
}

func New(runtime Runtime, adminToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", serveIndex)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, _ *http.Request) {
		snapshot := runtime.Snapshot()
		writeJSON(w, http.StatusOK, statusResponse{
			Status:          snapshot.Dataset.Status(),
			RuntimeRevision: snapshot.Revision,
			RuntimeLoadedAt: snapshot.LoadedAt.Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		snapshot := runtime.Snapshot()
		writeJSON(w, http.StatusOK, configResponse{
			Config:   snapshot.Config,
			Revision: snapshot.Revision,
			LoadedAt: snapshot.LoadedAt.Format(time.RFC3339),
		})
	})
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !authorized(r, adminToken) {
			writeError(w, http.StatusForbidden, fmt.Errorf("hot reload is limited to loopback clients unless ADMIN_TOKEN is configured"))
			return
		}
		var config runtimecfg.Config
		if err := decodeJSON(w, r, &config); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		snapshot, err := runtime.Apply(r.Context(), config)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err)
			return
		}
		writeJSON(w, http.StatusOK, configResponse{
			Config:   snapshot.Config,
			Revision: snapshot.Revision,
			LoadedAt: snapshot.LoadedAt.Format(time.RFC3339),
		})
	})
	mux.HandleFunc("POST /query", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w.Header().Set("Cache-Control", "no-store")
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if err := r.ParseForm(); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid query: %w", err))
			return
		}
		snapshot := runtime.Snapshot()
		program, err := routing.ParseGeo(r.Form.Get("rules"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := program.Validate(snapshot.Dataset); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		decision, err := program.Evaluate(r.Form.Get("value"), snapshot.Dataset)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		slog.DebugContext(r.Context(), "rule order diagnosed", "kind", decision.Kind, "rules", len(program.Rules), "matched", decision.Diagnostics.MatchedRules, "shadowed", decision.Diagnostics.ShadowedRules, "conflicting", decision.Diagnostics.ConflictingRules, "overlapping_sets", len(decision.Diagnostics.OverlappingSets), "revision", snapshot.Revision, "duration", time.Since(started))
		writeJSON(w, http.StatusOK, decision)
	})
	mux.HandleFunc("GET /api/geosite/{name}", func(w http.ResponseWriter, r *http.Request) {
		site, err := runtime.Snapshot().Dataset.GeoSite(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, site)
	})
	mux.HandleFunc("GET /api/lookup/domain", func(w http.ResponseWriter, r *http.Request) {
		value := r.URL.Query().Get("value")
		matches, err := runtime.Snapshot().Dataset.LookupDomain(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": value, "matches": matches})
	})
	mux.HandleFunc("GET /api/lookup/ip", func(w http.ResponseWriter, r *http.Request) {
		value := r.URL.Query().Get("value")
		matches, err := runtime.Snapshot().Dataset.LookupIP(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"query": value, "matches": matches})
	})
	mux.HandleFunc("GET /geosite", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, runtime.Snapshot().Dataset.GeoSiteIndex())
	})
	mux.HandleFunc("GET /geosite/{name}", func(w http.ResponseWriter, r *http.Request) {
		snapshot := runtime.Snapshot()
		body, err := snapshot.Dataset.RenderGeoSite(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		w.Header().Set("X-Surge-Geo-Regex-Mode", snapshot.Config.RegexMode)
		report := body.Report
		w.Header().Set("X-Surge-Geo-Exact-Regex", fmt.Sprintf("%d", report.Exact))
		w.Header().Set("X-Surge-Geo-Degraded-Regex", fmt.Sprintf("%d", report.Degraded))
		w.Header().Set("X-Surge-Geo-Skipped-Regex", fmt.Sprintf("%d", report.Skipped))
		writeRuleSet(w, r, body)
	})
	mux.HandleFunc("GET /geoip", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, runtime.Snapshot().Dataset.GeoIPIndex())
	})
	mux.HandleFunc("GET /geoip/{name}", func(w http.ResponseWriter, r *http.Request) {
		body, err := runtime.Snapshot().Dataset.RenderGeoIP(r.PathValue("name"))
		if err != nil {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeRuleSet(w, r, body)
	})
	return securityHeaders(mux)
}

func authorized(r *http.Request, adminToken string) bool {
	if adminToken != "" {
		expected := "Bearer " + adminToken
		actual := r.Header.Get("Authorization")
		return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
	}
	if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" {
		return false
	}
	requestHost := r.Host
	if host, _, err := net.SplitHostPort(requestHost); err == nil {
		requestHost = host
	}
	requestHost = strings.Trim(requestHost, "[]")
	requestIP := net.ParseIP(requestHost)
	if !strings.EqualFold(requestHost, "localhost") && (requestIP == nil || !requestIP.IsLoopback()) {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request body must contain one JSON object")
	}
	return nil
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, indexHTML)
}

func writeRuleSet(w http.ResponseWriter, r *http.Request, set rules.RuleSet) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600, stale-while-revalidate=86400")
	w.Header().Set("ETag", set.ETag)
	// ServeContent handles weak/list validators, HEAD and byte ranges without
	// copying or hashing the cached body, and retains cache headers on 304.
	http.ServeContent(w, r, "", time.Time{}, strings.NewReader(set.Body))
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
