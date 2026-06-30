package common

import (
	"fmt"
	"strings"
)

// ParseServerPath splits the "[server_name:]path" CLI positional used by
// both `bwfs list` and `rwfs list` on the first colon only, so paths that
// themselves contain colons (e.g. Windows "C:/Users/foo") pass through
// intact. An empty positional returns ("", "", nil) — no filter. A leading
// colon (":path") means no server filter, path-only. A trailing colon with
// nothing after it is a user error (empty path is not a valid filter once
// the positional is given at all).
func ParseServerPath(positional string) (serverName, path string, err error) {
	if positional == "" {
		return "", "", nil
	}

	idx := strings.Index(positional, ":")
	if idx == -1 {
		return "", positional, nil
	}

	serverName = positional[:idx]
	path = positional[idx+1:]
	if path == "" {
		return "", "", fmt.Errorf("path filter cannot be empty after ':' in %q", positional)
	}
	return serverName, path, nil
}
