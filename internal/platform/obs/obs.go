// Package obs holds observability plumbing: structured logging and the build
// version.
package obs

import (
	"log"
	"log/slog"
	"os"
	"strings"
)

// AppVersion is injected at build time:
//
//	-X github.com/tikhonp/proxier/internal/platform/obs.AppVersion=${APP_VERSION}
//
// and is "dev" for local builds.
var AppVersion = "dev"

// SetupLogging installs a JSON slog handler on stdout as the process-wide
// default and routes the stdlib log package through it, so every line is one
// machine-readable stream (Dozzle reads it).
func SetupLogging(level slog.Level) *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	log.SetFlags(0)
	log.SetOutput(slogWriter{logger})
	return logger
}

type slogWriter struct{ logger *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	w.logger.Info(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
