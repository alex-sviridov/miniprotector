package filesystem

import (
	"fmt"
)

// SplitByStreams divides files into the specified number of streams for parallel processing
func SplitByStreams(files []FileInfo, streams int) ([][]FileInfo, error) {
    if streams <= 0 {
        return nil, fmt.Errorf("streams must be positive, got %d", streams)
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

	return result, nil
}
