package protocol

import (
	"bytes"
	"encoding/gob"
	"fmt"

	"github.com/alex-sviridov/miniprotector/common/files"
)

// Encode serializes FileInfo to an efficient gob-encoded string
func Encode(fileInfo *files.FileInfo) (string, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(fileInfo); err != nil {
		return "", err
	}
	return fmt.Sprintf("FILE:%s\n", buf.String()), nil
}

// DecodeFileInfo deserializes FileInfo from gob-encoded string
func DecodeFileInfo(data string) (fileInfo files.FileInfo, err error) {
	// remove FILE: prefix in data string
	data = data[len("FILE:"):]
	buf := bytes.NewBufferString(data)
	dec := gob.NewDecoder(buf)
	err = dec.Decode(&fileInfo)
	return fileInfo, err
}
