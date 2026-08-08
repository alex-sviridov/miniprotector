package main

import "strings"

// splitPath derives (parentDir, shortFilename) from a stored path, choosing
// separator style from the path's own shape (leading "/" vs a drive-letter
// or UNC prefix) rather than the build platform's os.PathSeparator or a
// naive "last of either / or \" scan -- the latter mis-splits a Unix path
// containing a literal backslash in its filename (legal on Unix, illegal on
// Windows). catalog always runs on Linux, but Metadata is recorded verbatim
// by whichever OS backed the file up (fileinfo_windows.go/fileinfo_linux.go
// both store the raw os.Lstat path unmodified), so a Windows-origin path is
// fully backslash-separated and a Unix-origin path is fully
// forward-slash-separated -- path/filepath can't be used directly since it
// always splits on the build platform's separator.
//
// Root paths keep a trailing separator ("/", "C:\") rather than collapsing
// to "" -- "" is reserved elsewhere in this package to mean "unknown" (a
// Metadata decode failure), and a real root-level file must not read as
// unknown and get silently dropped by ListDirectoryFacets.
func splitPath(p string) (dir, base string) {
	if p == "" {
		return "", ""
	}
	sep := byte('/')
	if isWindowsStyle(p) {
		sep = '\\'
	}

	idx := strings.LastIndexByte(p, sep)
	if idx < 0 {
		return "", p // no separator: whole string is the name, no known parent
	}
	dir, base = p[:idx], p[idx+1:]

	if base == "" {
		return splitPath(p[:idx]) // tolerate a trailing separator, strip and retry
	}
	if dir == "" {
		dir = string(sep) // unix root: "/file.txt" -> "/"
	} else if isDriveRoot(dir) {
		dir += string(sep) // "C:" -> "C:\"
	}
	return dir, base
}

// isWindowsStyle reports whether p is UNC ("\\server\share\...") or
// drive-letter-rooted ("C:\..." / "C:/..."). Anything else -- including a
// bare relative path -- is treated as Unix-style, matching this system's
// object_filters convention that backup source paths are always absolute
// (see docs/api/rest-v1.md's "/var/www" vs "C:\..." examples).
func isWindowsStyle(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 2 && isDriveRoot(p[:2])
}

// isDriveRoot reports whether s is exactly a two-character drive letter
// prefix, e.g. "C:".
func isDriveRoot(s string) bool {
	return len(s) == 2 && s[1] == ':' && isASCIILetter(s[0])
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
