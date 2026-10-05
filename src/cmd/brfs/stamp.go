package main

import (
	"time"

	"github.com/alex-sviridov/miniprotector/retention"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// stamper resolves each file's expire_at from the job's compiled retention
// matrix. A nil stamper (no --retention-file) stamps nothing: 0 on the wire.
type stamper struct {
	m    *retention.Matcher
	root string
}

func (s *stamper) expireAt(file filesystem.FileInfo, now time.Time) int64 {
	if s == nil || s.m == nil {
		return 0
	}
	return s.m.ExpireAt(retention.Rel(s.root, file.Path()), now)
}
