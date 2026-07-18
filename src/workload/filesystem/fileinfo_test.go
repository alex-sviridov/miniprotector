package filesystem

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFileInfo_ExportedGetters(t *testing.T) {
	modTime := time.Now().Truncate(time.Second)
	fi := FileInfo{
		host:    "host-a",
		path:    "/var/log/syslog",
		name:    "syslog",
		size:    1234,
		mode:    fs.FileMode(0o644),
		owner:   1000,
		group:   1000,
		modTime: modTime,
	}

	assert.Equal(t, "/var/log/syslog", fi.Path())
	assert.Equal(t, uint32(1000), fi.Owner())
	assert.Equal(t, uint32(1000), fi.Group())
	assert.Equal(t, fs.FileMode(0o644), fi.Mode())
}
