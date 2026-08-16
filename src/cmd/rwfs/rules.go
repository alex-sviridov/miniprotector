// rules.go ports web/src/utils/restoreRules.js's resolveFile (longest-
// matching-ancestor-rule-wins) to Go, so `rwfs verify --rules-stdin` can
// resolve a restore policy's rules against a real ListFiles result without
// policy-server or agent ever needing to interpret rule semantics
// themselves. Kept behaviorally identical to the JS original -- see
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
package main

import "strings"

// RestoreRule mirrors policy-server's RestoreRule / the restore cart's rule
// shape -- {host, path, include, dest_path}. Host == "" means
// host-agnostic. DestPath, when non-empty and different from Path, is the
// path to restore to instead of Path -- see restoreDestPath, which
// interprets it uniformly for both file-level and folder-level rules.
type RestoreRule struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	DestPath  string `json:"dest_path"`
	NotBefore int64  `json:"not_before"`
	NotAfter  int64  `json:"not_after"`
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

// longestMatchingFolderRuleIndex finds the most specific host-agnostic
// folder rule covering path (checking path itself before its ancestors),
// returning its index into rules.
func longestMatchingFolderRuleIndex(rules []RestoreRule, path string) (ruleIndex int, include bool, found bool) {
	chain := ancestorsOrSelfRestorePath(path)
	for i := len(chain) - 1; i >= 0; i-- {
		for ri, r := range rules {
			if r.Host == "" && r.Path == chain[i] {
				return ri, r.Include, true
			}
		}
	}
	return -1, false, false
}

// resolveRestoreFileRule reports which rule governs (host, path): an exact
// host-specific rule wins outright (first such rule in rules, by index);
// otherwise the longest matching host-agnostic ancestor folder rule
// applies; found=false means no rule matched at all. Exposes the winning
// rule's index (unlike resolveRestoreFile's plain bool) so a caller can
// attribute a resolved file back to the specific rule -- and therefore
// timeframe -- that should govern it. See
// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md.
func resolveRestoreFileRule(rules []RestoreRule, host, path string) (ruleIndex int, include bool, found bool) {
	for i, r := range rules {
		if r.Host == host && r.Path == path {
			return i, r.Include, true
		}
	}
	return longestMatchingFolderRuleIndex(rules, path)
}

// resolveRestoreFile reports whether (host, path) is selected. Thin
// wrapper over resolveRestoreFileRule, kept for the pieces of this package
// that only need the bool -- see resolveRestoreFileRule for the precedence
// rules themselves.
func resolveRestoreFile(rules []RestoreRule, host, path string) bool {
	_, include, found := resolveRestoreFileRule(rules, host, path)
	return found && include
}

// restoreDestPath computes row's destination path under rule's dest_path
// rename, if any. Works uniformly for a file-level rule (rowPath ==
// rule.Path exactly, a straight swap) and a folder-level rule (rowPath is
// always a rule.Path-prefixed descendant, by construction of
// longestMatchingFolderRuleIndex's ancestor-chain match) -- the "single
// replacement prefix for the whole folder" semantics
// docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md
// already specified for a future executor to interpret.
func restoreDestPath(rule RestoreRule, rowPath string) string {
	if rule.DestPath == "" || rule.DestPath == rule.Path {
		return rowPath
	}
	// For a root folder rule (rule.Path == "/"), TrimPrefix strips the
	// separator itself along with the prefix, leaving a suffix with no
	// leading "/" -- restore it so the concatenation below doesn't glue
	// DestPath directly onto suffix with no separator between them.
	suffix := strings.TrimPrefix(rowPath, rule.Path)
	if suffix != "" && !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	return rule.DestPath + suffix
}
