package files

import (
	"bytes"
	"encoding/gob"
)

// Encode serializes FileInfo to an efficient gob-encoded string
func Encode(fileInfo *FileInfo) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(fileInfo); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeFileInfo deserializes FileInfo from gob-encoded string
func DecodeFileInfo(data []byte) (fileInfo *FileInfo, err error) {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	err = dec.Decode(&fileInfo)
	return fileInfo, err
}
