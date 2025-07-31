package protocol

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/network"
)

func SendFileMetadata(stream *network.Stream, file *files.FileInfo) (fileNeeded bool, err error) {
	// Send FileType rune, Size int64, Mode, Owner, Group uint32, ModTime, AccessTime, ChangeTime time.Time, Path string
	message := fmt.Sprintf(
		"FILE:%c;%d;%d;%d;%d;%d;%d;%d;%s",
		file.FileType,
		file.Size,
		file.Mode,
		file.Owner,
		file.Group,
		file.ModTime.Unix(),
		file.AccessTime.Unix(),
		file.ChangeTime.Unix(),
		file.Path)

	response, err := stream.SendMessage(message)
	if err != nil {
		return false, fmt.Errorf("cannot send message: %v", err)
	}

	switch response {
	case "SEND_FILE":
		return true, nil
	case "SKIP_FILE":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected response: %s", response)
	}
}

// ParseFileMetadata reconstructs FileInfo object from message
func ParseFileMetadata(message string) (*files.FileInfo, error) {
	// Check if message starts with "FILE:"
	if !strings.HasPrefix(message, "FILE:") {
		return nil, fmt.Errorf("invalid message format: missing FILE: prefix")
	}

	// Extract payload after "FILE:"
	payload := message[5:]
	if len(payload) == 0 {
		return nil, fmt.Errorf("invalid message format: empty payload")
	}

	// Split into exactly 9 parts: FileType + 7 numeric fields + path
	// This preserves semicolons in the path
	parts := strings.SplitN(payload, ";", 9)

	if len(parts) != 9 {
		return nil, fmt.Errorf("invalid message format: expected 9 fields, got %d", len(parts))
	}

	// Parse FileType (single character)
	if len(parts[0]) != 1 {
		return nil, fmt.Errorf("invalid filetype field: must be single character")
	}
	fileType := rune(parts[0][0])

	// Parse numeric fields
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid size: %v", err)
	}

	mode, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid mode: %v", err)
	}

	owner, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid owner: %v", err)
	}

	group, err := strconv.ParseUint(parts[4], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid group: %v", err)
	}

	modTimeUnix, err := strconv.ParseInt(parts[5], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid modtime: %v", err)
	}
	modTime := time.Unix(modTimeUnix, 0)

	accessTimeUnix, err := strconv.ParseInt(parts[6], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid accesstime: %v", err)
	}
	accessTime := time.Unix(accessTimeUnix, 0)

	changeTimeUnix, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid changetime: %v", err)
	}
	changeTime := time.Unix(changeTimeUnix, 0)

	path := parts[8]
	//TODO: Reconstruct file, like putting attributes based on filetype
	//TODO: Remove link counter from fileinfo
	//TODO: Add linktarget into message or make it optional
	return &files.FileInfo{
		FileType:   fileType,
		Size:       size,
		Mode:       uint32(mode),
		Owner:      uint32(owner),
		Group:      uint32(group),
		ModTime:    modTime,
		AccessTime: accessTime,
		ChangeTime: changeTime,
		Path:       path,
	}, nil
}
