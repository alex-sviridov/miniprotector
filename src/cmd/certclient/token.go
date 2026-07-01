package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// hasExistingIdentity reports whether all three identity files are already
// present in certsDir.
func hasExistingIdentity(certsDir string) bool {
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		if _, err := os.Stat(filepath.Join(certsDir, name)); err != nil {
			return false
		}
	}
	return true
}

// resolveToken returns the enrollment token from, in preference order: the
// --token flag (least safe — visible via process listings), MP_CERT_TOKEN,
// or a line read from stdin.
func resolveToken(flagValue string, stdin io.Reader) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv("MP_CERT_TOKEN"); env != "" {
		return env, nil
	}
	fmt.Fprint(os.Stderr, "Enter enrollment token: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", fmt.Errorf("no token provided via --token, MP_CERT_TOKEN, or stdin")
	}
	return token, nil
}
