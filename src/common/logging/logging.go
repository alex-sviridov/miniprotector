package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

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

// nopCloser satisfies io.Closer with a no-op Close -- returned by NewLogger
// when no file handler was created (log_dir unset or unwritable), so
// callers can always safely `defer logfile.Close()` without risking a
// nil-interface panic.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func getLevel(debugMode bool) slog.Level {
	if debugMode {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func NewLogger(ctx context.Context) (*slog.Logger, io.Closer) {
	conf := config.GetConfigFromContext(ctx)

	level := getLevel(ctx.Value("debugMode").(bool))
	quietMode := ctx.Value("quietMode").(bool)
	appName := ctx.Value("appName").(string)

	var logFile io.Closer = nopCloser{}
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

	// File output (JSON format): one stable, rotated file per binary name --
	// <log_dir>/<appName>.log -- not one file per process invocation.
	// Optional: don't fail startup if the directory is unavailable.
	if conf.LogDir != "" {
		if err := os.MkdirAll(conf.LogDir, 0755); err == nil {
			ljLogger := &lumberjack.Logger{
				Filename:   filepath.Join(conf.LogDir, appName+".log"),
				MaxSize:    50, // megabytes
				MaxBackups: 5,
				MaxAge:     14, // days
				Compress:   true,
			}
			handler.fileHandler = slog.NewJSONHandler(ljLogger, &slog.HandlerOptions{
				Level:     level,
				AddSource: level == slog.LevelDebug,
			})
			logFile = ljLogger
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

	return logger, logFile
}
