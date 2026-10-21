package main

import (
	"os"
	"regexp"
	"strings"
	"time"
)

// anchorMinLen is the shortest rendered line usable as a scroll anchor; shorter
// ones ("", "-", "1.") are too ambiguous to find again after a re-render.
const anchorMinLen = 4

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// fileSig identifies a version of the file on disk.
type fileSig struct {
	mtime time.Time
	size  int64
	fi    os.FileInfo
}

// clampOffset restricts a scroll offset to what the view can show.
func clampOffset(off, lines, height int) int {
	return max(min(off, lines-height+1), 0)
}

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

func normalizeLine(s string) string {
	return strings.TrimSpace(stripANSI(s))
}

// anchorLine returns the text of the line at off if it is distinctive enough to
// locate again after a re-render, or "".
func anchorLine(lines []string, off int) string {
	if off < 0 || off >= len(lines) {
		return ""
	}
	l := normalizeLine(lines[off])
	if len(l) < anchorMinLen {
		return ""
	}
	return l
}

// findAnchor returns the index of the line matching anchor that is nearest to
// old (the earlier one on a tie), or old when there is no match.
func findAnchor(lines []string, anchor string, old int) int {
	if anchor == "" {
		return old
	}
	best := -1
	for i, l := range lines {
		if normalizeLine(l) != anchor {
			continue
		}
		if best < 0 || abs(i-old) < abs(best-old) {
			best = i
		}
	}
	if best < 0 {
		return old
	}
	return best
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func statSig(path string) (fileSig, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileSig{}, err
	}
	return fileSig{mtime: fi.ModTime(), size: fi.Size(), fi: fi}, nil
}

// isStale tells whether the file, as observed now (or with err), differs from
// the version that was loaded.
func isStale(loaded, now fileSig, err error) bool {
	if err != nil {
		return true
	}
	return !now.mtime.Equal(loaded.mtime) || now.size != loaded.size || !os.SameFile(loaded.fi, now.fi)
}

// overlayText returns the text of the bottom-right indicator, by priority:
// message, deleted, stale.
func overlayText(stale, deleted bool, msg string) string {
	switch {
	case msg != "":
		return " " + msg + " "
	case deleted:
		return " file deleted - press R "
	case stale:
		return " stale - press R "
	}
	return ""
}
