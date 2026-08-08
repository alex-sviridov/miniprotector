package main

import "testing"

func TestSplitPath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		wantDir  string
		wantBase string
	}{
		{"unix nested", "/var/lib/dbdata/data.db", "/var/lib/dbdata", "data.db"},
		{"unix root", "/data.db", "/", "data.db"},
		{"unix filename containing a literal backslash", `/var/log/weird\file.txt`, "/var/log", `weird\file.txt`},
		{"windows nested", `C:\Users\alice\Documents\file.txt`, `C:\Users\alice\Documents`, "file.txt"},
		{"windows drive root", `C:\file.txt`, `C:\`, "file.txt"},
		{"windows drive root with forward slashes", "C:/Users/alice/file.txt", "C:/Users/alice", "file.txt"},
		{"unc nested", `\\server\share\folder\file.txt`, `\\server\share\folder`, "file.txt"},
		{"unc minimal", `\\server\share\file.txt`, `\\server\share`, "file.txt"},
		{"no separator", "data.db", "", "data.db"},
		{"empty string", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, base := splitPath(c.path)
			if dir != c.wantDir || base != c.wantBase {
				t.Errorf("splitPath(%q) = (%q, %q), want (%q, %q)", c.path, dir, base, c.wantDir, c.wantBase)
			}
		})
	}
}
