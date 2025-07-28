package common

import (
	"os"
)

func IsDirectory(filepath string) (bool, error) {
	info, err := os.Stat(filepath)
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}
