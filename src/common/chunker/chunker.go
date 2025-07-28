package chunker

import (
	"encoding/hex"
	"io"
	"os"
	"lukechampine.com/blake3"
)

type Chunk struct {
	Data     []byte
	Checksum string
}

// ChunkFile returns a single chunk containing the entire file (MVP version)
// TODO: Implement proper chunking algorithm later
func ChunkFile(filepath string) ([]Chunk, error) {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return nil, err
	}

	checksum := calculateChecksum(data)

	chunk := Chunk{
		Data:     data,
		Checksum: checksum,
	}

	return []Chunk{chunk}, nil
}

// CalculateFileChecksum calculates BLAKE3 checksum without loading entire file
func CalculateFileChecksum(filepath string) (string, error) {
	file, err := os.Open(filepath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := blake3.New(8, nil)
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	// Convert to hex string instead of raw bytes
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func calculateChecksum(data []byte) string {
	hasher := blake3.New(8, nil)
	hasher.Write(data)
	// Convert to hex string instead of raw bytes
	return hex.EncodeToString(hasher.Sum(nil))
}
