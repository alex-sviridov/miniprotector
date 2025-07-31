package files

import (
	"io/fs"
	"path/filepath"
)

// ListRecursive traverses directory tree and returns file information
func ListRecursive(sourcePath string) ([]FileInfo, error) {
	var items []FileInfo

	err := filepath.WalkDir(sourcePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		fileInfo, err := getFileInfo(path)
		if err != nil {
			return err
		}

		items = append(items, fileInfo)
		return nil
	})

	return items, err
}

// SplitByStreams divides files into the specified number of streams for parallel processing
func SplitByStreams(files []FileInfo, streams int) [][]FileInfo {
	if streams <= 0 {
		return nil
	}

	result := make([][]FileInfo, streams)
	filesPerStream := len(files) / streams
	remainder := len(files) % streams

	start := 0
	for i := 0; i < streams; i++ {
		chunkSize := filesPerStream
		if i < remainder {
			chunkSize++
		}

		end := start + chunkSize
		result[i] = files[start:end]
		start = end
	}

	return result
}
