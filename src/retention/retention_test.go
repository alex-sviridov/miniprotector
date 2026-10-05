package retention

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const day = int64(86400)

func TestBuild_NoRulesIsJustTheDefaultRow(t *testing.T) {
	m := Build(nil, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestBuild_DropsRulesThatCannotOverlapTheRoot(t *testing.T) {
	m := Build([]Rule{{Prefix: "/etc", KeepSeconds: 1}}, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestBuild_SegmentBoundaryNotStringPrefix(t *testing.T) {
	// /data2 must not be treated as under /data
	m := Build([]Rule{{Prefix: "/data2", KeepSeconds: 1}}, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestBuild_RewritesRulesBelowRootRelativeToRoot(t *testing.T) {
	m := Build([]Rule{{Prefix: "/data/tmp", Include: []string{"*.log"}, KeepSeconds: 3 * day}}, "/data", 7*day)
	assert.Equal(t, Matrix{
		{Prefix: "tmp", Include: []string{"*.log"}, KeepSeconds: 3 * day},
		{Prefix: "", KeepSeconds: 7 * day},
	}, m)
}

func TestBuild_RuleCoveringRootBecomesEmptyPrefix(t *testing.T) {
	m := Build([]Rule{{Prefix: "/", Include: []string{"*.tmp"}, KeepSeconds: day}}, "/data", 7*day)
	assert.Equal(t, Matrix{
		{Prefix: "", Include: []string{"*.tmp"}, KeepSeconds: day},
		{Prefix: "", KeepSeconds: 7 * day},
	}, m)
}

func TestBuild_TruncatesAfterFirstUnconditionalCoveringRow(t *testing.T) {
	m := Build([]Rule{
		{Prefix: "/data", KeepSeconds: 30 * day},
		{Prefix: "/data/x", KeepSeconds: 1},
	}, "/data", 7*day)
	assert.Equal(t, Matrix{{Prefix: "", KeepSeconds: 30 * day}}, m)
}

func TestBuild_RootSlash(t *testing.T) {
	m := Build([]Rule{{Prefix: "/var/log", KeepSeconds: day}}, "/", 7*day)
	assert.Equal(t, Matrix{{Prefix: "var/log", KeepSeconds: day}, {Prefix: "", KeepSeconds: 7 * day}}, m)
}

func TestCompile_RejectsBadGlobAndBadPrefixAndNegativeKeep(t *testing.T) {
	_, err := Compile(Matrix{{Include: []string{"[abc"}}})
	assert.Error(t, err)
	_, err = Compile(Matrix{{Prefix: "/abs"}})
	assert.Error(t, err)
	_, err = Compile(Matrix{{Prefix: "a/../b"}})
	assert.Error(t, err)
	_, err = Compile(Matrix{{KeepSeconds: -1}})
	assert.Error(t, err)
}

func TestExpireAt_FirstMatchWinsAndSegmentBoundary(t *testing.T) {
	m, err := Compile(Matrix{
		{Prefix: "tmp", KeepSeconds: 3 * day},
		{Prefix: "", Include: []string{"*.log"}, KeepSeconds: 30 * day},
		{Prefix: "", KeepSeconds: 7 * day},
	})
	require.NoError(t, err)
	now := time.Unix(1_000_000, 0)

	assert.Equal(t, now.Unix()+3*day, m.ExpireAt("tmp/a.txt", now))
	assert.Equal(t, now.Unix()+3*day, m.ExpireAt("tmp", now))
	assert.Equal(t, now.Unix()+30*day, m.ExpireAt("tmpfile/a.log", now)) // not under tmp/
	assert.Equal(t, now.Unix()+7*day, m.ExpireAt("docs/readme.md", now))
}

func TestExpireAt_ZeroKeepMeansNever(t *testing.T) {
	m, err := Compile(Matrix{{Prefix: "keep", KeepSeconds: 0}, {Prefix: "", KeepSeconds: day}})
	require.NoError(t, err)
	assert.Equal(t, int64(0), m.ExpireAt("keep/x", time.Unix(5, 0)))
}

func TestExpireAt_EmptyMatrixIsUnset(t *testing.T) {
	m, err := Compile(nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), m.ExpireAt("a", time.Unix(5, 0)))
}

func TestRel(t *testing.T) {
	assert.Equal(t, "a/b", Rel("/data", "/data/a/b"))
	assert.Equal(t, "", Rel("/data", "/data"))
}

func TestWriteFileLoadFile_RoundTripAndMalformed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "m.json")
	require.NoError(t, WriteFile(p, Matrix{{Prefix: "a", KeepSeconds: day}, {KeepSeconds: 7 * day}}))
	m, err := LoadFile(p)
	require.NoError(t, err)
	assert.Equal(t, int64(100)+day, m.ExpireAt("a/x", time.Unix(100, 0)))

	_, err = LoadFile(filepath.Join(t.TempDir(), "missing.json"))
	assert.Error(t, err)
}

func BenchmarkExpireAt_TenRows(b *testing.B) {
	rows := Matrix{}
	for i := 0; i < 9; i++ {
		rows = append(rows, Row{Prefix: "dir" + string(rune('a'+i)), Include: []string{"*.log"}, KeepSeconds: day})
	}
	rows = append(rows, Row{KeepSeconds: 7 * day})
	m, _ := Compile(rows)
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.ExpireAt("other/deep/path/file.txt", now)
	}
}

func TestLoadFile_MalformedJSONAndInvalidRowsAreErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{not json"), 0o644))
	_, err := LoadFile(bad)
	assert.Error(t, err)

	invalid := filepath.Join(dir, "invalid.json")
	require.NoError(t, os.WriteFile(invalid, []byte(`[{"prefix":"/abs","keep_seconds":1}]`), 0o644))
	_, err = LoadFile(invalid)
	assert.Error(t, err)
}
