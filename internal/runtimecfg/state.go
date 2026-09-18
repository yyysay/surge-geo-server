// Package runtimecfg owns the atomically replaceable runtime snapshot.
package runtimecfg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yyysay/surge-geo-server/internal/rules"
	"github.com/yyysay/surge-geo-server/internal/source"
)

type Config struct {
	GeoSiteURL string `json:"geosite_url"`
	GeoIPURL   string `json:"geoip_url"`
	RegexMode  string `json:"regex_mode"`
}

type Snapshot struct {
	Dataset  *rules.Dataset `json:"-"`
	Config   Config         `json:"config"`
	Revision uint64         `json:"revision"`
	LoadedAt time.Time      `json:"loaded_at"`
}

type Options struct {
	DataDir    string
	ConfigFile string
	Initial    Config
	Fetcher    *source.Fetcher
}

type State struct {
	mu          sync.Mutex
	current     atomic.Pointer[Snapshot]
	fetcher     *source.Fetcher
	dataDir     string
	configFile  string
	geositePath string
	geoipPath   string
	configHash  [sha256.Size]byte
}

type sourceCandidate struct {
	dir            string
	geositePath    string
	geoipPath      string
	geositeChanged bool
	geoipChanged   bool
}

func New(ctx context.Context, options Options) (*State, error) {
	if options.DataDir == "" {
		return nil, fmt.Errorf("data directory is required")
	}
	if options.ConfigFile == "" {
		options.ConfigFile = filepath.Join(options.DataDir, "runtime.json")
	}
	if options.Fetcher == nil {
		options.Fetcher = source.NewFetcher()
	}
	state := &State{
		fetcher:     options.Fetcher,
		dataDir:     options.DataDir,
		configFile:  options.ConfigFile,
		geositePath: filepath.Join(options.DataDir, "sources", "geosite.dat"),
		geoipPath:   filepath.Join(options.DataDir, "sources", "geoip.dat"),
	}
	if err := os.MkdirAll(options.DataDir, 0o755); err != nil {
		return nil, err
	}

	config := options.Initial
	configRaw, err := os.ReadFile(options.ConfigFile)
	configExists := err == nil
	if configExists {
		if err := json.Unmarshal(configRaw, &config); err != nil {
			return nil, fmt.Errorf("read runtime config: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	config, mode, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	candidate, syncErr := state.prepareSourceCandidate(ctx, config)
	geositePath, geoipPath := state.geositePath, state.geoipPath
	if candidate != nil {
		defer candidate.cleanup()
		geositePath, geoipPath = candidate.geositePath, candidate.geoipPath
	}
	snapshot, err := state.buildSnapshot(config, mode, 1, geositePath, geoipPath)
	if err != nil && candidate != nil {
		syncErr = err
		candidate = nil
		snapshot, err = state.buildSnapshot(config, mode, 1, state.geositePath, state.geoipPath)
	}
	if err != nil {
		if syncErr != nil {
			return nil, fmt.Errorf("initial source sync: %v; load local data: %w", syncErr, err)
		}
		return nil, err
	}
	if syncErr != nil {
		slog.Warn("initial source update failed; using validated local data", "error", syncErr)
	}
	if candidate != nil {
		if err := state.commitSourceCandidate(candidate); err != nil {
			return nil, fmt.Errorf("commit initial source data: %w", err)
		}
	}
	state.current.Store(snapshot)
	if configExists {
		state.configHash = sha256.Sum256(configRaw)
	} else {
		raw, err := state.persistConfig(config)
		if err != nil {
			return nil, err
		}
		state.configHash = sha256.Sum256(raw)
	}
	return state, nil
}

func (s *State) Snapshot() *Snapshot {
	return s.current.Load()
}

// Apply validates and builds a complete candidate before atomically publishing it.
func (s *State) Apply(ctx context.Context, config Config) (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyLocked(ctx, config, true, [sha256.Size]byte{})
}

func (s *State) applyLocked(ctx context.Context, config Config, persist bool, fileHash [sha256.Size]byte) (*Snapshot, error) {
	config, mode, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	current := s.current.Load()
	if current != nil && config == current.Config {
		slog.DebugContext(ctx, "runtime config unchanged", "revision", current.Revision)
		if persist {
			raw, err := s.persistConfig(config)
			if err != nil {
				return nil, err
			}
			s.configHash = sha256.Sum256(raw)
		} else {
			s.configHash = fileHash
		}
		return current, nil
	}
	geositePath, geoipPath := s.geositePath, s.geoipPath
	changedGeoSite := current == nil || config.GeoSiteURL != current.Config.GeoSiteURL
	changedGeoIP := current == nil || config.GeoIPURL != current.Config.GeoIPURL

	tempDir := ""
	if changedGeoSite || changedGeoIP {
		tempDir, err = os.MkdirTemp(s.dataDir, ".runtime-reload-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(tempDir)
	}
	if changedGeoSite {
		geositePath = filepath.Join(tempDir, "geosite.dat")
		if _, err := s.fetcher.Sync(ctx, config.GeoSiteURL, geositePath); err != nil {
			return nil, fmt.Errorf("sync geosite candidate: %w", err)
		}
	}
	if changedGeoIP {
		geoipPath = filepath.Join(tempDir, "geoip.dat")
		if _, err := s.fetcher.Sync(ctx, config.GeoIPURL, geoipPath); err != nil {
			return nil, fmt.Errorf("sync geoip candidate: %w", err)
		}
	}

	revision := uint64(1)
	if current != nil {
		revision = current.Revision + 1
	}
	candidate, err := s.buildSnapshot(config, mode, revision, geositePath, geoipPath)
	if err != nil {
		return nil, err
	}
	if changedGeoSite {
		if err := commitSource(geositePath, s.geositePath, true); err != nil {
			return nil, err
		}
	}
	if changedGeoIP {
		if err := commitSource(geoipPath, s.geoipPath, true); err != nil {
			return nil, err
		}
	}
	if persist {
		raw, err := s.persistConfig(config)
		if err != nil {
			return nil, err
		}
		s.configHash = sha256.Sum256(raw)
	} else {
		s.configHash = fileHash
	}
	s.current.Store(candidate)
	slog.InfoContext(ctx, "runtime config applied", "revision", candidate.Revision, "regex_mode", candidate.Config.RegexMode)
	return candidate, nil
}

// ReloadConfig checks runtime.json and hot-applies it when its contents changed.
func (s *State) ReloadConfig(ctx context.Context) (bool, error) {
	raw, err := os.ReadFile(s.configFile)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if hash == s.configHash {
		return false, nil
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return false, fmt.Errorf("read runtime config: %w", err)
	}
	before := s.current.Load()
	if _, err := s.applyLocked(ctx, config, false, hash); err != nil {
		return false, err
	}
	return s.current.Load() != before, nil
}

// Refresh updates upstream files and publishes a new dataset snapshot when needed.
func (s *State) Refresh(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.current.Load()
	candidateSources, err := s.prepareSourceCandidate(ctx, current.Config)
	if err != nil {
		return false, err
	}
	defer candidateSources.cleanup()
	if !candidateSources.geositeChanged && !candidateSources.geoipChanged {
		// A 200 with identical bytes (or a 304) may still update validators.
		return false, s.commitSourceCandidate(candidateSources)
	}
	_, mode, err := normalizeConfig(current.Config)
	if err != nil {
		return false, err
	}
	candidate, err := s.buildSnapshot(
		current.Config,
		mode,
		current.Revision+1,
		candidateSources.geositePath,
		candidateSources.geoipPath,
	)
	if err != nil {
		return false, fmt.Errorf("validate refreshed source data: %w", err)
	}
	if err := s.commitSourceCandidate(candidateSources); err != nil {
		return false, fmt.Errorf("commit refreshed source data: %w", err)
	}
	s.current.Store(candidate)
	return true, nil
}

func (s *State) buildSnapshot(config Config, mode rules.RegexMode, revision uint64, geositePath, geoipPath string) (*Snapshot, error) {
	started := time.Now()
	geositeData, err := os.ReadFile(geositePath)
	if err != nil {
		return nil, fmt.Errorf("read geosite data: %w", err)
	}
	geoipData, err := os.ReadFile(geoipPath)
	if err != nil {
		return nil, fmt.Errorf("read geoip data: %w", err)
	}
	dataset, err := rules.New(geositeData, geoipData, mode)
	if err != nil {
		return nil, fmt.Errorf("build dataset: %w", err)
	}
	status := dataset.Status()
	if status.GeoSiteSets == 0 {
		return nil, fmt.Errorf("build dataset: geosite data contains no rule sets")
	}
	if status.GeoIPSets == 0 {
		return nil, fmt.Errorf("build dataset: geoip data contains no rule sets")
	}
	slog.Debug("dataset validated", "revision", revision, "geosite_bytes", len(geositeData), "geoip_bytes", len(geoipData), "duration", time.Since(started))
	return &Snapshot{
		Dataset:  dataset,
		Config:   config,
		Revision: revision,
		LoadedAt: time.Now().UTC(),
	}, nil
}

func (s *State) prepareSourceCandidate(ctx context.Context, config Config) (*sourceCandidate, error) {
	dir, err := os.MkdirTemp(s.dataDir, ".source-refresh-")
	if err != nil {
		return nil, err
	}
	candidate := &sourceCandidate{
		dir:         dir,
		geositePath: filepath.Join(dir, "geosite.dat"),
		geoipPath:   filepath.Join(dir, "geoip.dat"),
	}
	fail := func(err error) (*sourceCandidate, error) {
		candidate.cleanup()
		return nil, err
	}
	if err := stageSource(s.geositePath, candidate.geositePath); err != nil {
		return fail(fmt.Errorf("stage geosite cache: %w", err))
	}
	if err := stageSource(s.geoipPath, candidate.geoipPath); err != nil {
		return fail(fmt.Errorf("stage geoip cache: %w", err))
	}
	candidate.geositeChanged, err = s.fetcher.Sync(ctx, config.GeoSiteURL, candidate.geositePath)
	if err != nil {
		return fail(fmt.Errorf("sync geosite candidate: %w", err))
	}
	candidate.geoipChanged, err = s.fetcher.Sync(ctx, config.GeoIPURL, candidate.geoipPath)
	if err != nil {
		return fail(fmt.Errorf("sync geoip candidate: %w", err))
	}
	return candidate, nil
}

func (s *State) commitSourceCandidate(candidate *sourceCandidate) error {
	if err := commitSource(candidate.geositePath, s.geositePath, candidate.geositeChanged); err != nil {
		return fmt.Errorf("commit geosite: %w", err)
	}
	if err := commitSource(candidate.geoipPath, s.geoipPath, candidate.geoipChanged); err != nil {
		return fmt.Errorf("commit geoip: %w", err)
	}
	return nil
}

func (candidate *sourceCandidate) cleanup() {
	_ = os.RemoveAll(candidate.dir)
}

func stageSource(sourcePath, candidatePath string) error {
	for _, suffix := range []string{"", ".meta.json"} {
		// Sync only replaces files by rename, so hard links safely avoid copying
		// the entire cache before every conditional request.
		err := os.Link(sourcePath+suffix, candidatePath+suffix)
		if err == nil || os.IsNotExist(err) {
			continue
		}
		if err := copyAtomic(sourcePath+suffix, candidatePath+suffix); err != nil {
			return err
		}
	}
	return nil
}

func commitSource(candidatePath, destinationPath string, changed bool) error {
	if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
		return err
	}
	if changed {
		if err := replaceSource(candidatePath, destinationPath); err != nil {
			return err
		}
	}
	return replaceSource(candidatePath+".meta.json", destinationPath+".meta.json")
}

func replaceSource(candidatePath, destinationPath string) error {
	err := os.Rename(candidatePath, destinationPath)
	if errors.Is(err, syscall.EXDEV) {
		// sources/ can be a separate mount; still replace atomically there.
		return copyAtomic(candidatePath, destinationPath)
	}
	return err
}

func (s *State) persistConfig(config Config) ([]byte, error) {
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if err := writeAtomic(s.configFile, raw, 0o600); err != nil {
		return nil, err
	}
	return raw, nil
}

func normalizeConfig(config Config) (Config, rules.RegexMode, error) {
	config.GeoSiteURL = strings.TrimSpace(config.GeoSiteURL)
	config.GeoIPURL = strings.TrimSpace(config.GeoIPURL)
	for label, value := range map[string]string{"geosite_url": config.GeoSiteURL, "geoip_url": config.GeoIPURL} {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return Config{}, "", fmt.Errorf("%s must be an HTTP(S) URL without credentials", label)
		}
	}
	mode, err := rules.ParseRegexMode(config.RegexMode)
	if err != nil {
		return Config{}, "", err
	}
	config.RegexMode = string(mode)
	return config, mode, nil
}

func copyAtomic(sourcePath, destinationPath string) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer input.Close()
	return writeReaderAtomic(destinationPath, input, 0o644)
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	return writeReaderAtomic(path, bytes.NewReader(raw), mode)
}

func writeReaderAtomic(path string, input io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, input); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
