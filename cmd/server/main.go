package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yangtudou/surge-geo-server/internal/httpapi"
	"github.com/yangtudou/surge-geo-server/internal/rules"
	"github.com/yangtudou/surge-geo-server/internal/source"
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
	refresh := flag.Duration("refresh", envDuration("REFRESH_INTERVAL", 6*time.Hour), "upstream refresh interval")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	geoSitePath := filepath.Join(*dataDir, "sources", "geosite.dat")
	geoIPPath := filepath.Join(*dataDir, "sources", "geoip.dat")
	fetcher := source.NewFetcher()
	if _, err := syncSources(ctx, fetcher, *geositeURL, *geoipURL, geoSitePath, geoIPPath); err != nil {
		slog.Error("initial source sync failed", "error", err)
		if !filesExist(geoSitePath, geoIPPath) {
			os.Exit(1)
		}
		slog.Warn("using cached source files")
	}

	var current atomic.Pointer[rules.Dataset]
	load := func() error {
		geositeData, err := os.ReadFile(geoSitePath)
		if err != nil {
			return err
		}
		geoipData, err := os.ReadFile(geoIPPath)
		if err != nil {
			return err
		}
		dataset, err := rules.New(geositeData, geoipData)
		if err != nil {
			return err
		}
		current.Store(dataset)
		status := dataset.Status()
		slog.Info("datasets loaded", "geosite_sets", status.GeoSiteSets, "geoip_sets", status.GeoIPSets)
		return nil
	}
	if err := load(); err != nil {
		slog.Error("load datasets", "error", err)
		os.Exit(1)
	}

	go func() {
		ticker := time.NewTicker(*refresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				changed, err := syncSources(ctx, fetcher, *geositeURL, *geoipURL, geoSitePath, geoIPPath)
				if err != nil {
					slog.Warn("refresh failed", "error", err)
					continue
				}
				if !changed {
					continue
				}
				if err := load(); err != nil {
					slog.Warn("reload failed", "error", err)
				}
			}
		}
	}()

	handler := httpapi.New(func() *rules.Dataset { return current.Load() })
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

func syncSources(ctx context.Context, fetcher *source.Fetcher, geositeURL, geoipURL, geositePath, geoipPath string) (bool, error) {
	geositeChanged, err := fetcher.Sync(ctx, geositeURL, geositePath)
	if err != nil {
		return false, fmt.Errorf("sync geosite: %w", err)
	}
	geoipChanged, err := fetcher.Sync(ctx, geoipURL, geoipPath)
	if err != nil {
		return false, fmt.Errorf("sync geoip: %w", err)
	}
	return geositeChanged || geoipChanged, nil
}

func filesExist(paths ...string) bool {
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
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
