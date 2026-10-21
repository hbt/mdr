package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClampOffset(t *testing.T) {
	tests := []struct {
		name               string
		off, lines, height int
		want               int
	}{
		{"content shorter than view", 5, 10, 24, 0},
		{"negative", -3, 100, 24, 0},
		{"in range", 10, 100, 24, 10},
		{"past end", 500, 100, 24, 77},
		{"exactly last", 77, 100, 24, 77},
		{"empty", 3, 0, 24, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampOffset(tt.off, tt.lines, tt.height); got != tt.want {
				t.Errorf("clampOffset(%d,%d,%d) = %d, want %d", tt.off, tt.lines, tt.height, got, tt.want)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"plain", "hello world", "hello world"},
		{"sgr", "\x1b[1;31mred\x1b[0m text", "red text"},
		{"256 color", "\x1b[38;5;208mx\x1b[m", "x"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripANSI(tt.in); got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestAnchorLine(t *testing.T) {
	lines := []string{"", "    \x1b[1mHello World\x1b[0m   ", "ab", "plain line"}
	tests := []struct {
		name string
		off  int
		want string
	}{
		{"normalized", 1, "Hello World"},
		{"blank", 0, ""},
		{"too short", 2, ""},
		{"plain", 3, "plain line"},
		{"negative offset", -1, ""},
		{"past end", 99, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anchorLine(lines, tt.off); got != tt.want {
				t.Errorf("anchorLine(off=%d) = %q, want %q", tt.off, got, tt.want)
			}
		})
	}
}

func TestFindAnchor(t *testing.T) {
	tests := []struct {
		name   string
		lines  []string
		anchor string
		old    int
		want   int
	}{
		{"exact", []string{"aaaa", "bbbb", "cccc"}, "bbbb", 0, 1},
		{"ansi and padding ignored", []string{"aaaa", "  \x1b[1mbbbb\x1b[0m "}, "bbbb", 0, 1},
		{"duplicates pick nearest", []string{"dup!", "x", "x", "dup!", "x", "x", "x", "dup!"}, "dup!", 4, 3},
		{"duplicates tie picks earlier", []string{"dup!", "x", "dup!"}, "dup!", 1, 0},
		{"no match keeps old", []string{"aaaa", "bbbb"}, "zzzz", 1, 1},
		{"empty anchor keeps old", []string{"aaaa", "bbbb"}, "", 1, 1},
		{"lines inserted above", []string{"new1", "new2", "new3", "aaaa", "target"}, "target", 1, 4},
		{"lines removed above", []string{"target", "tail"}, "target", 5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findAnchor(tt.lines, tt.anchor, tt.old); got != tt.want {
				t.Errorf("findAnchor = %d, want %d", got, tt.want)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestStatSig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	mt := time.Unix(1700000000, 0)
	writeFile(t, p, "hello", mt)

	sig, err := statSig(p)
	if err != nil {
		t.Fatal(err)
	}
	if sig.size != 5 || !sig.mtime.Equal(mt) || sig.fi == nil {
		t.Errorf("unexpected sig: %+v", sig)
	}
	if _, err := statSig(filepath.Join(dir, "missing.md")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestIsStale(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	mt := time.Unix(1700000000, 0)
	writeFile(t, p, "hello", mt)
	loaded, err := statSig(p)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("unchanged", func(t *testing.T) {
		now, err := statSig(p)
		if isStale(loaded, now, err) {
			t.Error("unchanged file reported stale")
		}
	})

	t.Run("mtime change", func(t *testing.T) {
		writeFile(t, p, "hello", mt.Add(time.Minute))
		now, err := statSig(p)
		if !isStale(loaded, now, err) {
			t.Error("mtime change not detected")
		}
		writeFile(t, p, "hello", mt)
	})

	t.Run("size change", func(t *testing.T) {
		writeFile(t, p, "hello!!", mt)
		now, err := statSig(p)
		if !isStale(loaded, now, err) {
			t.Error("size change not detected")
		}
		writeFile(t, p, "hello", mt)
	})

	t.Run("rename replace same size and mtime", func(t *testing.T) {
		tmp := filepath.Join(dir, "a.md.tmp")
		writeFile(t, tmp, "world", mt)
		if err := os.Rename(tmp, p); err != nil {
			t.Fatal(err)
		}
		now, err := statSig(p)
		if !isStale(loaded, now, err) {
			t.Error("different inode not detected")
		}
	})

	t.Run("deleted", func(t *testing.T) {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		now, err := statSig(p)
		if err == nil {
			t.Fatal("expected stat error")
		}
		if !isStale(loaded, now, err) {
			t.Error("deleted file not reported stale")
		}
	})
}

func TestOverlayText(t *testing.T) {
	tests := []struct {
		name           string
		stale, deleted bool
		msg, want      string
	}{
		{"nothing", false, false, "", ""},
		{"stale", true, false, "", " stale - press R "},
		{"deleted", true, true, "", " file deleted - press R "},
		{"deleted without stale flag", false, true, "", " file deleted - press R "},
		{"msg beats stale", true, false, "stdin: cannot reload", " stdin: cannot reload "},
		{"msg beats deleted", true, true, "boom", " boom "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overlayText(tt.stale, tt.deleted, tt.msg); got != tt.want {
				t.Errorf("overlayText = %q, want %q", got, tt.want)
			}
		})
	}
}
