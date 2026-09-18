package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLogLevels(t *testing.T) {
	for _, level := range []string{"info", "debug", " DEBUG "} {
		t.Run(level, func(t *testing.T) {
			var output bytes.Buffer
			logger, err := newLogger(level, &output)
			if err != nil {
				t.Fatal(err)
			}
			logger.Debug("details")
			logger.Info("event")
			logger.Warn("warning")
			logger.Error("failure")
			text := output.String()
			if strings.Contains(text, "level=DEBUG") != (strings.TrimSpace(strings.ToLower(level)) == "debug") {
				t.Fatalf("unexpected debug visibility: %s", text)
			}
			for _, severity := range []string{"INFO", "WARN", "ERROR"} {
				if !strings.Contains(text, "level="+severity) {
					t.Fatalf("missing %s: %s", severity, text)
				}
			}
		})
	}
	for _, level := range []string{"", "verbose", "error"} {
		if _, err := newLogger(level, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid level %q", level)
		}
	}
}
