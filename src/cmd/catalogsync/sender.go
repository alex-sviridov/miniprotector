package main

import (
	"log/slog"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

// Sender delivers a batch of file version records to the backup catalog.
// The only implementation today is LoggingSender; a real gRPC client
// against the future catalog service replaces it later behind this
// interface — nothing else in catalogsync needs to change when that
// happens.
type Sender interface {
	Send(batch []wfs.FileVersionRecord) error
}

// LoggingSender logs every batch it's given and always succeeds — a
// stand-in for the not-yet-built catalog client, proving the replication
// pipeline end-to-end.
type LoggingSender struct {
	logger *slog.Logger
}

func NewLoggingSender(logger *slog.Logger) *LoggingSender {
	return &LoggingSender{logger: logger}
}

func (s *LoggingSender) Send(batch []wfs.FileVersionRecord) error {
	for _, r := range batch {
		s.logger.Info("catalog replication entry", "job_id", r.JobID, "object_id", r.ObjectID, "seq", r.Seq)
	}
	s.logger.Info("catalog replication batch sent", "count", len(batch))
	return nil
}
