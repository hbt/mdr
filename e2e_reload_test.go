package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end tests for the R (reload) key and the stale-file indicator.

// tempDoc writes content to a fresh file and returns its path.
func tempDoc(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	writeDoc(t, path, content)
	return path
}

func writeDoc(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// numbered returns n "Line NNN" paragraphs, optionally preceded by a prefix.
func numbered(prefix string, n int) string {
	var sb strings.Builder
	sb.WriteString(prefix)
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "Line %03d\n\n", i)
	}
	return sb.String()
}

func runDoc(t *testing.T, path string) *pane {
	return start(t, fmt.Sprintf("%q %q", binPath, path))
}

// rows splits a capture-pane screen into exactly one entry per pane row.
func rows(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }

// lastRow returns the stale/message indicator row (the last pane row) and
// its starting column, or "" if the last row is empty.
func lastRow(s string) (string, int) {
	r := rows(s)
	row := strings.TrimRight(r[len(r)-1], " ")
	return strings.TrimSpace(row), len(row) - len(strings.TrimLeft(row, " "))
}

func TestReloadShowsNewContent(t *testing.T) {
	path := tempDoc(t, "# Alpha Original\n")
	p := runDoc(t, path)
	p.waitText("Alpha Original")

	writeDoc(t, path, "# Beta Changed\n")
	p.send("R")
	s := p.waitText("Beta Changed")
	if strings.Contains(s, "Alpha Original") {
		t.Errorf("old content still visible:\n%s", s)
	}
}

func TestReloadKeepsScrollPosition(t *testing.T) {
	path := tempDoc(t, numbered("# Head\n\n", 100))
	p := runDoc(t, path)
	p.waitText("Head")
	p.send("PageDown", "PageDown")
	s := p.waitFor("scrolled", func(s string) bool { lo, _ := lineNumbers(s); return lo > 5 })

	// land on a row whose first line is text, not a blank paragraph gap
	for first(s) == "" {
		prev := s
		p.send("j")
		s = p.waitFor("one row down", func(s string) bool { return s != prev })
	}
	top := first(s)

	// same content: top row must not move
	p.send("R")
	if got := p.settle(); first(got) != top {
		t.Errorf("top row changed on plain reload: %q -> %q", top, first(got))
	}

	// lines inserted above the viewport: same top text must stay
	writeDoc(t, path, "# Head\n\n"+insertedAbove()+numbered("", 100))
	p.waitText("stale - press R")
	p.send("R")
	p.waitFor("indicator cleared", func(s string) bool { return !strings.Contains(s, "stale - press R") })
	if got := p.settle(); first(got) != top {
		t.Errorf("top row after insert above: %q, want %q\n%s", first(got), top, got)
	}
	for i := 0; i < 20; i++ {
		p.send("PageUp")
	}
	p.waitText("Inserted 1")
}

func insertedAbove() string {
	var sb strings.Builder
	for i := 1; i <= 7; i++ {
		fmt.Fprintf(&sb, "Inserted %d\n\n", i)
	}
	return sb.String()
}

// first returns the trimmed first pane row.
func first(s string) string { return strings.TrimSpace(rows(s)[0]) }

func TestReloadShorterFileClamps(t *testing.T) {
	path := tempDoc(t, numbered("# Head\n\n", 100))
	p := runDoc(t, path)
	p.waitText("Head")
	for i := 0; i < 40; i++ {
		p.send("PageDown")
	}
	p.waitText("Line 100")

	writeDoc(t, path, "# Tiny Doc\n\nonly this\n")
	p.send("R")
	s := p.waitText("Tiny Doc")
	if !strings.Contains(s, "only this") {
		t.Errorf("short content not fully visible (offset not clamped):\n%s", s)
	}
}

func TestStaleIndicatorAppearsAndClears(t *testing.T) {
	path := tempDoc(t, "# Alpha Original\n")
	p := runDoc(t, path)
	s := p.waitText("Alpha Original")
	if row, _ := lastRow(s); row != "" {
		t.Fatalf("indicator shown before any change: %q", row)
	}

	writeDoc(t, path, "# Beta Changed, longer now\n")
	s = p.waitText("stale - press R")
	row, col := lastRow(s)
	if row != "stale - press R" {
		t.Errorf("last row = %q, want the stale indicator", row)
	}
	// the pane trims the trailing space, so the text ends on the last column
	if want := paneW - len("stale - press R") - 1; col != want {
		t.Errorf("indicator at column %d, want right-aligned at %d:\n%s", col, want, s)
	}
	if !strings.Contains(s, "Alpha Original") {
		t.Errorf("content must not change before R:\n%s", s)
	}

	p.send("R")
	p.waitText("Beta Changed")
	p.waitFor("indicator cleared", func(s string) bool { return !strings.Contains(s, "stale - press R") })
	// stays cleared
	if s := p.settle(); strings.Contains(s, "stale") {
		t.Errorf("indicator came back after reload:\n%s", s)
	}
}

func TestStaleIndicatorAfterResize(t *testing.T) {
	path := tempDoc(t, "# Alpha Original\n")
	p := runDoc(t, path)
	p.waitText("Alpha Original")
	writeDoc(t, path, "# Beta Changed, longer now\n")
	p.waitText("stale - press R")

	if out, err := exec.Command("tmux", "-L", p.socket, "resize-window", "-x", "60", "-y", "12").CombinedOutput(); err != nil {
		t.Skipf("resize-window unsupported: %v\n%s", err, out)
	}
	s := p.waitFor("indicator on last row", func(s string) bool {
		r := rows(s)
		return len(r) == 12 && strings.Contains(r[11], "stale - press R")
	})
	if _, col := lastRow(s); col != 60-len("stale - press R")-1 {
		t.Errorf("indicator not right-aligned at width 60 (col %d):\n%s", col, s)
	}
}

func TestDeletedFileIndicatorAndReloadKeepsContent(t *testing.T) {
	path := tempDoc(t, "# Alpha Original\n")
	p := runDoc(t, path)
	p.waitText("Alpha Original")

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	p.waitText("file deleted - press R")

	p.send("R")
	s := p.waitFor("read error message", func(s string) bool {
		row, _ := lastRow(s)
		return row != "" && !strings.Contains(row, "file deleted")
	})
	if !strings.Contains(s, "Alpha Original") {
		t.Errorf("old content lost after failed reload:\n%s", s)
	}

	// still alive and responsive; file coming back clears things on R
	writeDoc(t, path, "# Gamma Restored\n")
	p.send("R")
	p.waitText("Gamma Restored")
	p.waitFor("indicator cleared", func(s string) bool { r, _ := lastRow(s); return r == "" })
	p.send("q")
	if code := p.waitExit(); code != "0" {
		t.Errorf("exit code = %q, want 0", code)
	}
}

func TestReloadStdinMessage(t *testing.T) {
	p := start(t, fmt.Sprintf("cat %q | %q", fixture("basic.md"), binPath))
	p.waitText("Hello Basic")
	p.send("R")
	s := p.waitText("stdin: cannot reload")
	if !strings.Contains(s, "Hello Basic") {
		t.Errorf("content lost:\n%s", s)
	}
	p.send("q")
	p.waitExit()
}

// The window grows while scrolled to the end: the offset must be re-clamped
// without needing another key press, so the end of the document stays on the
// last row instead of leaving blank rows below it.
func TestResizeGrowReclampsWithoutKey(t *testing.T) {
	p := run(t, "long.md")
	p.waitText("Top Heading")
	for i := 0; i < 40; i++ {
		p.send("PageDown")
	}
	p.waitText("Bottom Marker")
	s := p.settle()
	rowOf := func(s string) int {
		for i, r := range rows(s) {
			if strings.Contains(r, "Bottom Marker") {
				return i
			}
		}
		return -1
	}
	before := rowOf(s)
	beforeRows := len(rows(s))
	if out, err := exec.Command("tmux", "-L", p.socket, "resize-window", "-x", "80", "-y", "40").CombinedOutput(); err != nil {
		t.Skipf("resize-window unsupported: %v\n%s", err, out)
	}
	s = p.waitFor("resized", func(s string) bool { return len(rows(s)) == 40 })
	s = p.settle()
	if got, want := rowOf(s), before+len(rows(s))-beforeRows; got != want {
		t.Errorf("Bottom Marker on row %d after grow, want %d (blank rows below?):\n%s", got, want, s)
	}
}
