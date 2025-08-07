package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
)

type contextKey string

const ContextKey contextKey = "logger"

func GetLoggerFromContext(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(ContextKey).(*slog.Logger)
	if !ok {
		return nil
	}
	return logger
}

type multiHandler struct {
	consoleHandler slog.Handler
	fileHandler    slog.Handler
}

func (mh *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return (mh.consoleHandler != nil && mh.consoleHandler.Enabled(ctx, level)) ||
		(mh.fileHandler != nil && mh.fileHandler.Enabled(ctx, level))
}

func (mh *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	if mh.consoleHandler != nil && mh.consoleHandler.Enabled(ctx, record.Level) {
		if err := mh.consoleHandler.Handle(ctx, record.Clone()); err != nil {
			return err
		}
	}
	if mh.fileHandler != nil && mh.fileHandler.Enabled(ctx, record.Level) {
		if err := mh.fileHandler.Handle(ctx, record.Clone()); err != nil {
			return err
		}
	}
	return nil
}

func (mh *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newHandler := &multiHandler{}
	if mh.consoleHandler != nil {
		newHandler.consoleHandler = mh.consoleHandler.WithAttrs(attrs)
	}
	if mh.fileHandler != nil {
		newHandler.fileHandler = mh.fileHandler.WithAttrs(attrs)
	}
	return newHandler
}

func (mh *multiHandler) WithGroup(name string) slog.Handler {
	newHandler := &multiHandler{}
	if mh.consoleHandler != nil {
		newHandler.consoleHandler = mh.consoleHandler.WithGroup(name)
	}
	if mh.fileHandler != nil {
		newHandler.fileHandler = mh.fileHandler.WithGroup(name)
	}
	return newHandler
}

func getLevel(debugMode bool) slog.Level {
	if debugMode {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func NewLogger(ctx context.Context) (*slog.Logger, io.Closer, error) {
	conf := config.GetConfigFromContext(ctx)

	level := getLevel(ctx.Value("debugMode").(bool))
	quietMode := ctx.Value("quietMode").(bool)
	appName := ctx.Value("appName").(string)

	var logFile *os.File
	handler := &multiHandler{}

	// Console output (logfmt format, only if not quiet)
	if !quietMode {
		handler.consoleHandler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level:     level,
			AddSource: level == slog.LevelDebug,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					return slog.String(a.Key, a.Value.Time().Format("15:04:05"))
				}
				return a
			},
		})
	}

	// File output (JSON format, optional - don't fail if unavailable)
	if conf.LogFolder != "" {
		if err := os.MkdirAll(conf.LogFolder, 0755); err == nil {
			filename := fmt.Sprintf("%s-%s.%d.log", appName, time.Now().Format("2006-01-02"), os.Getpid())
			if file, err := os.OpenFile(filepath.Join(conf.LogFolder, filename), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
				handler.fileHandler = slog.NewJSONHandler(file, &slog.HandlerOptions{
					Level:     level,
					AddSource: level == slog.LevelDebug,
				})
				logFile = file
			}
		}
	}

	// Fallback to discard if no handlers
	if handler.consoleHandler == nil && handler.fileHandler == nil {
		handler.consoleHandler = slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level})
	}

	logger := slog.New(handler).With(
		slog.String("app", appName),
		slog.Int("pid", os.Getpid()),
	)

	if jobId := ctx.Value("jobId"); jobId != nil {
		logger = logger.With(slog.String("job_id", jobId.(string)))
	}

	return logger, logFile, nil
}
