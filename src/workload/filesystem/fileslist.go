package filesystem

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/workload"
)

type FilesList []FileInfo

func Discover(path string) (FilesList, error) {
	result := FilesList{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("source path does not exist: %s", path)
	}

	hostname := common.GetHostname()

	err := filepath.WalkDir(path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("failed to walk dir %s: %w", path, err)
		}

		fileInfo, err := getFileInfo(path)
		fileInfo.host = hostname
		if err != nil {
			return fmt.Errorf("failed to get file info %s: %w", path, err)
		}

		result = append(result, fileInfo)
		return nil
	})

	return result, err
}

func (fl FilesList) WithIncludes(patterns ...string) workload.BackupObjectsList {
	if len(patterns) == 0 {
		return fl
	}
	result := FilesList{}
	for _, file := range fl {
		for _, pattern := range patterns {
			if file.match(pattern) {
				result = append(result, file)
				break
			}
		}
	}
	return result
}

func (fl FilesList) WithExcludes(patterns ...string) workload.BackupObjectsList {
	if len(patterns) == 0 {
		return fl
	}
	result := FilesList{}
fileLoop:
	for _, file := range fl {
		for _, pattern := range patterns {
			if file.match(pattern) {
				continue fileLoop
			}
		}
		result = append(result, file)
	}
	return result
}
