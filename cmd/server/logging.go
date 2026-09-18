package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

func newLogger(level string, output io.Writer) (*slog.Logger, error) {
	var minimum slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info":
		minimum = slog.LevelInfo
	case "debug":
		minimum = slog.LevelDebug
	default:
		return nil, fmt.Errorf("log-level must be info or debug, got %q", level)
	}
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{Level: minimum})), nil
}
