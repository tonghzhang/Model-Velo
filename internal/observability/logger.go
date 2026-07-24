package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"model-velo/internal/config"
)

func NewLogger(settings config.Observability) *slog.Logger {
	level := slog.LevelInfo
	switch settings.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	options := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if settings.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, options)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, options)
	}
	return slog.New(handler).With("service", settings.ServiceName)
}

// LogWriter lets legacy standard-library log calls share the same structured
// sink while they are progressively converted to direct slog calls.
func LogWriter(logger *slog.Logger, component string) io.Writer {
	return &structuredWriter{logger: logger.With("component", component)}
}

type structuredWriter struct {
	logger *slog.Logger
}

func (writer *structuredWriter) Write(payload []byte) (int, error) {
	message := strings.TrimSpace(string(payload))
	if message != "" {
		writer.logger.Info(message)
	}
	return len(payload), nil
}
