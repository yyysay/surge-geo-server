package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yyysay/surge-geo-server/internal/httpapi"
	"github.com/yyysay/surge-geo-server/internal/rules"
	"github.com/yyysay/surge-geo-server/internal/runtimecfg"
)

const (
	defaultGeoSiteURL = "https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat"
	defaultGeoIPURL   = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip-lite.dat"
)

func main() {
	listen := flag.String("listen", env("LISTEN", ":8080"), "HTTP listen address")
	dataDir := flag.String("data-dir", env("DATA_DIR", "./data"), "persistent data directory")
	geositeURL := flag.String("geosite-url", env("GEOSITE_URL", defaultGeoSiteURL), "geosite DAT URL")
	geoipURL := flag.String("geoip-url", env("GEOIP_URL", defaultGeoIPURL), "geoip DAT URL")
	regexModeValue := flag.String("regex-mode", env("REGEX_MODE", string(rules.RegexStrict)), "regex downgrade mode (strict or balanced)")
	refresh := flag.Duration("refresh", envDuration("REFRESH_INTERVAL", 6*time.Hour), "upstream refresh interval")
	configFile := flag.String("config-file", env("RUNTIME_CONFIG", ""), "runtime JSON config path (defaults to DATA_DIR/runtime.json)")
	configWatch := flag.Duration("config-watch", envDuration("CONFIG_WATCH_INTERVAL", 2*time.Second), "runtime config file watch interval")
	adminToken := flag.String("admin-token", env("ADMIN_TOKEN", ""), "bearer token for remote hot reload")
	flag.Parse()
	if *refresh <= 0 || *configWatch <= 0 {
		slog.Error("refresh and config-watch intervals must be positive")
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	state, err := runtimecfg.New(ctx, runtimecfg.Options{
		DataDir:    *dataDir,
		ConfigFile: *configFile,
		Initial: runtimecfg.Config{
			GeoSiteURL: *geositeURL,
			GeoIPURL:   *geoipURL,
			RegexMode:  *regexModeValue,
		},
	})
	if err != nil {
		slog.Error("initialize runtime", "error", err)
		os.Exit(1)
	}
	loaded := state.Snapshot()
	status := loaded.Dataset.Status()
	slog.Info("runtime loaded", "revision", loaded.Revision, "geosite_sets", status.GeoSiteSets, "geoip_sets", status.GeoIPSets)

	go func() {
		refreshTicker := time.NewTicker(*refresh)
		configTicker := time.NewTicker(*configWatch)
		defer refreshTicker.Stop()
		defer configTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-refreshTicker.C:
				slog.Info("upstream refresh started")
				changed, err := state.Refresh(ctx)
				if err != nil {
					slog.Error("upstream refresh rejected; keeping current data", "error", err)
				} else if changed {
					loaded := state.Snapshot()
					status := loaded.Dataset.Status()
					slog.Info("upstream refresh completed", "result", "updated", "revision", loaded.Revision, "geosite_sets", status.GeoSiteSets, "geoip_sets", status.GeoIPSets)
				} else {
					slog.Info("upstream refresh completed", "result", "unchanged", "revision", state.Snapshot().Revision)
				}
			case <-configTicker.C:
				changed, err := state.ReloadConfig(ctx)
				if err != nil {
					slog.Error("runtime config reload rejected; keeping current snapshot", "error", err)
				} else if changed {
					slog.Info("runtime config hot-reloaded", "revision", state.Snapshot().Revision)
				}
			}
		}
	}()

	handler := httpapi.New(state, *adminToken)
	server := &http.Server{Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		slog.Info("server started", "listen", *listen)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		slog.Warn("invalid duration environment variable", "name", name, "value", value)
		return fallback
	}
	return duration
}
