// rules.go ports web/src/utils/restoreRules.js's resolveFile (longest-
// matching-ancestor-rule-wins) to Go, so `rwfs verify --rules-stdin` can
// resolve a restore policy's rules against a real ListFiles result without
// policy-server or agent ever needing to interpret rule semantics
// themselves. Kept behaviorally identical to the JS original -- see
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
package main

import "strings"

// RestoreRule mirrors policy-server's RestoreRule / the restore cart's rule
// shape -- {host, path, include}. Host == "" means host-agnostic.
type RestoreRule struct {
	Host    string `json:"host"`
	Path    string `json:"path"`
	Include bool   `json:"include"`
}

// splitRestorePath derives (parent, base) from p, mirroring
// cmd/catalog/pathsplit.go's splitPath exactly (duplicated here -- rwfs
// can't import cmd/catalog, another command's main package). Root paths
// keep a trailing separator; "" means "no known parent" (p had no
// separator at all).
func splitRestorePath(p string) (parent, base string) {
	if p == "" {
		return "", ""
	}
	sep := byte('/')
	if isWindowsStyleRestorePath(p) {
		sep = '\\'
		if !strings.ContainsRune(p, '\\') {
			sep = '/'
		}
	}
	idx := strings.LastIndexByte(p, sep)
	if idx < 0 {
		return "", p
	}
	parent, base = p[:idx], p[idx+1:]
	if base == "" {
		return splitRestorePath(p[:idx])
	}
	if parent == "" {
		parent = string(sep)
	} else if isDriveRootRestorePath(parent) {
		parent += string(sep)
	}
	return parent, base
}

func isWindowsStyleRestorePath(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 2 && isDriveRootRestorePath(p[:2])
}

func isDriveRootRestorePath(s string) bool {
	return len(s) == 2 && s[1] == ':' && isASCIILetterRestorePath(s[0])
}

func isASCIILetterRestorePath(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// ancestorsOrSelfRestorePath returns path's ancestor chain, root first,
// path itself last -- mirrors web/src/utils/pathSplit.js's pathCrumbs, but
// returning only the path strings (this package never needs display
// names).
func ancestorsOrSelfRestorePath(path string) []string {
	var chain []string
	current := path
	for current != "" {
		chain = append(chain, current)
		parent, _ := splitRestorePath(current)
		if parent == current {
			break // true root reached (splitRestorePath returns itself unchanged at a drive/UNC root)
		}
		current = parent
	}
	// reverse into root-first order
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// longestMatchingFolderRule finds the most specific host-agnostic folder
// rule covering path (checking path itself before its ancestors), mirrors
// restoreRules.js's function of the same name.
func longestMatchingFolderRule(rules []RestoreRule, path string) (include bool, found bool) {
	chain := ancestorsOrSelfRestorePath(path)
	for i := len(chain) - 1; i >= 0; i-- {
		for _, r := range rules {
			if r.Host == "" && r.Path == chain[i] {
				return r.Include, true
			}
		}
	}
	return false, false
}

// resolveRestoreFile reports whether (host, path) is selected: an exact
// host-specific rule wins outright; otherwise the longest matching
// host-agnostic ancestor folder rule applies; no match = unselected.
// Mirrors restoreRules.js's resolveFile exactly.
func resolveRestoreFile(rules []RestoreRule, host, path string) bool {
	for _, r := range rules {
		if r.Host == host && r.Path == path {
			return r.Include
		}
	}
	include, found := longestMatchingFolderRule(rules, path)
	return found && include
}
