package filesystem

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/alex-sviridov/miniprotector/common"
)

type FilesList []FileInfo

// Discover walks root, returning one FileInfo per surviving file and
// directory. exclude is checked first: a directory matching any exclude
// pattern is pruned entirely (skipped, along with everything beneath it);
// a file matching any exclude pattern is omitted. include is then checked
// for files only -- a file is kept only if it matches at least one include
// pattern; directories are never filtered by include, so traversal always
// continues into non-excluded directories, and a directory entry that
// survives the exclude check is always emitted.
//
// A pattern with no "/" matches an entry's basename at any depth; a
// pattern containing "/" matches the entry's path relative to root. An
// empty include list matches no files -- callers that want "match
// everything" must pass a pattern (e.g. []string{"*"}) explicitly; this
// function applies no default of its own.
func Discover(root string, include, exclude []string) (FilesList, error) {
	result := FilesList{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil, fmt.Errorf("source path does not exist: %s", root)
	}

	hostname := common.GetHostname()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("failed to walk dir %s: %w", path, err)
		}

		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, relErr)
		}

		if matchesAny(exclude, relPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.IsDir() && !matchesAny(include, relPath) {
			return nil
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

// matchesAny reports whether relPath matches any pattern: a pattern with
// no "/" is matched against relPath's basename (so it matches at any
// depth); a pattern containing "/" is matched against relPath itself.
func matchesAny(patterns []string, relPath string) bool {
	base := filepath.Base(relPath)
	for _, pattern := range patterns {
		target := base
		if strings.Contains(pattern, "/") {
			target = relPath
		}
		if matched, _ := filepath.Match(pattern, target); matched {
			return true
		}
	}
	return false
}
