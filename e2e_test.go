package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end tests: build the real binary and drive it inside a tmux pane
// (gocui needs a tty). Screens are read back with `tmux capture-pane -p`.

const (
	paneW   = 80
	paneH   = 24
	timeout = 10 * time.Second
)

var (
	binPath  string
	sockSeq  int32
	exitCode = "EXIT:"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mdr-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := 1
	func() {
		defer os.RemoveAll(dir)
		if _, err := exec.LookPath("tmux"); err != nil {
			// tests skip themselves individually
			code = m.Run()
			return
		}
		binPath = filepath.Join(dir, "mdr")
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
			return
		}
		code = m.Run()
	}()
	os.Exit(code)
}

type pane struct {
	t      *testing.T
	socket string
}

// start runs `shellCmd` in a fresh tmux server/pane. The command is followed by
// an EXIT:<code> marker so tests can observe process termination, and a sleep
// so the pane stays alive for capturing.
func start(t *testing.T, shellCmd string) *pane {
	t.Helper()
	if binPath == "" {
		t.Skip("tmux not installed")
	}
	p := &pane{t: t, socket: fmt.Sprintf("mdr-e2e-%d-%d", os.Getpid(), atomic.AddInt32(&sockSeq, 1))}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", p.socket, "kill-server").Run() })

	full := fmt.Sprintf("%s; echo %s$?; sleep 60", shellCmd, exitCode)
	out, err := exec.Command("tmux", "-L", p.socket, "-f", "/dev/null", "new-session", "-d",
		"-x", fmt.Sprint(paneW), "-y", fmt.Sprint(paneH), "-e", "TERM=xterm-256color", "sh", "-c", full).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	return p
}

func (p *pane) screen() string {
	out, _ := exec.Command("tmux", "-L", p.socket, "capture-pane", "-p").Output()
	return string(out)
}

func (p *pane) send(keys ...string) {
	p.t.Helper()
	args := append([]string{"-L", p.socket, "send-keys"}, keys...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		p.t.Fatalf("send-keys %v: %v\n%s", keys, err, out)
	}
}

// waitFor polls until pred(screen) is true and returns that screen.
func (p *pane) waitFor(desc string, pred func(string) bool) string {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	var s string
	for time.Now().Before(deadline) {
		s = p.screen()
		if pred(s) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.t.Fatalf("timeout waiting for %s; last screen:\n%s", desc, s)
	return ""
}

func (p *pane) waitText(text string) string {
	p.t.Helper()
	return p.waitFor(fmt.Sprintf("%q", text), func(s string) bool { return strings.Contains(s, text) })
}

// settle waits until the screen is identical across consecutive polls, to
// assert "nothing more changes" without a fixed sleep guess.
func (p *pane) settle() string {
	p.t.Helper()
	prev := p.screen()
	stable := 0
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(40 * time.Millisecond)
		cur := p.screen()
		if cur == prev {
			stable++
			if stable >= 5 {
				return cur
			}
		} else {
			stable = 0
			prev = cur
		}
	}
	p.t.Fatalf("screen never settled:\n%s", prev)
	return ""
}

func (p *pane) waitExit() string {
	p.t.Helper()
	s := p.waitText(exitCode)
	i := strings.Index(s, exitCode)
	return strings.TrimSpace(strings.SplitN(s[i+len(exitCode):], "\n", 2)[0])
}

func fixture(name string) string {
	abs, _ := filepath.Abs(filepath.Join("testdata", name))
	return abs
}

func run(t *testing.T, fixtureName string) *pane {
	return start(t, fmt.Sprintf("%q %q", binPath, fixture(fixtureName)))
}

// firstLine returns the lowest "Line NNN" number visible, or -1.
func lineNumbers(s string) (lo, hi int) {
	lo, hi = -1, -1
	for _, l := range strings.Split(s, "\n") {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(l), "Line %d", &n); err == nil {
			if lo < 0 {
				lo = n
			}
			hi = n
		}
	}
	return
}

func TestRendersHeading(t *testing.T) {
	p := run(t, "basic.md")
	s := p.waitText("Hello Basic")
	for _, want := range []string{"Second Heading", "item one", "item two", "bold"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
}

func TestScrollDownUp(t *testing.T) {
	p := run(t, "long.md")
	p.waitText("Top Heading")
	if s := p.screen(); strings.Contains(s, "Bottom Marker") {
		t.Fatalf("long fixture should not fit on one screen:\n%s", s)
	}
	_, hi0 := lineNumbers(p.screen())

	p.send("j", "j", "j")
	s := p.waitFor("scrolled by 3", func(s string) bool { return !strings.Contains(s, "Top Heading") })
	_, hi1 := lineNumbers(s)
	if hi1 <= hi0 {
		t.Errorf("down did not reveal later content: hi %d -> %d", hi0, hi1)
	}

	p.send("k", "k", "k")
	p.waitText("Top Heading")
}

func TestArrowKeys(t *testing.T) {
	p := run(t, "long.md")
	p.waitText("Top Heading")
	p.send("Down", "Down", "Down")
	p.waitFor("heading scrolled off", func(s string) bool { return !strings.Contains(s, "Top Heading") })
	p.send("Up", "Up", "Up")
	p.waitText("Top Heading")
}

func TestPageDownUp(t *testing.T) {
	p := run(t, "long.md")
	p.waitText("Top Heading")
	_, hi0 := lineNumbers(p.screen())

	p.send("PageDown")
	s := p.waitFor("page down", func(s string) bool { return !strings.Contains(s, "Top Heading") })
	lo1, hi1 := lineNumbers(s)
	// page = half a screen (12 rows); content is 2 rows per "Line N"
	if lo1 <= 1 || hi1 <= hi0 {
		t.Errorf("page down: unexpected window %d-%d (was ..%d)\n%s", lo1, hi1, hi0, s)
	}

	p.send("Space")
	p.waitFor("space pages down", func(s string) bool { lo, _ := lineNumbers(s); return lo > lo1 })

	p.send("PageUp", "PageUp")
	p.waitText("Top Heading")
}

func TestClampAtEnds(t *testing.T) {
	p := run(t, "long.md")
	top := p.waitText("Top Heading")

	// up at top: no change
	p.send("k", "PageUp", "Up")
	if s := p.settle(); s != top {
		t.Errorf("scrolling up at top changed screen:\n%s", s)
	}

	// page down far past the end
	for i := 0; i < 40; i++ {
		p.send("PageDown")
	}
	bottom := p.waitText("Bottom Marker")
	p.send("PageDown", "j", "Down", "Space")
	if s := p.settle(); s != bottom {
		t.Errorf("scrolling down at end changed screen:\n--before--\n%s\n--after--\n%s", bottom, s)
	}
	if !strings.Contains(bottom, "Line 120") {
		t.Errorf("last content not visible at end:\n%s", bottom)
	}

	// and back up to the top: 40 PageUps must clamp at 0, not go negative
	for i := 0; i < 40; i++ {
		p.send("PageUp")
	}
	p.waitText("Top Heading")
	if s := p.settle(); s != top {
		t.Errorf("did not return to the exact top screen:\n%s", s)
	}
}

func TestQuitKeys(t *testing.T) {
	for _, key := range []string{"q", "C-c"} {
		t.Run(key, func(t *testing.T) {
			p := run(t, "basic.md")
			p.waitText("Hello Basic")
			p.send(key)
			if code := p.waitExit(); code != "0" {
				t.Errorf("exit code = %q, want 0", code)
			}
			if s := p.screen(); strings.Contains(s, "Hello Basic") {
				t.Errorf("content still on screen after quit (UI not closed):\n%s", s)
			}
		})
	}
}

func TestMissingFile(t *testing.T) {
	p := start(t, fmt.Sprintf("%q /nonexistent/nope.md", binPath))
	if code := p.waitExit(); code != "1" {
		t.Errorf("exit code = %q, want 1", code)
	}
	if s := p.screen(); !strings.Contains(s, "error while reading file") {
		t.Errorf("expected error message, got:\n%s", s)
	}
}

func TestTooManyArgs(t *testing.T) {
	p := start(t, fmt.Sprintf("%q a.md b.md", binPath))
	if code := p.waitExit(); code != "1" {
		t.Errorf("exit code = %q, want 1", code)
	}
	p.waitText("only one file is supported")
}

func TestNoArgsOnTTYUsage(t *testing.T) {
	p := start(t, fmt.Sprintf("%q", binPath))
	if code := p.waitExit(); code != "1" {
		t.Errorf("exit code = %q, want 1", code)
	}
	p.waitText("usage:")
}

func TestStdinPipe(t *testing.T) {
	// stdin is a pipe, stdout/tty is the pane; gocui opens /dev/tty-less
	// input from stdin by default, so this documents actual behavior.
	p := start(t, fmt.Sprintf("cat %q | %q", fixture("basic.md"), binPath))
	p.waitText("Hello Basic")
	p.send("q")
	p.waitExit()
}

func TestCodeBlockAndRelativeImage(t *testing.T) {
	p := run(t, "code.md")
	s := p.waitText("Code Sample")
	for _, want := range []string{"Marker", "After image"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen missing %q:\n%s", want, s)
		}
	}
}

func TestRelativeImageResolvedFromFileDir(t *testing.T) {
	// mdr chdirs to the file's directory, so ./pixel.png must resolve even
	// when launched from elsewhere. Missing image would render alt text only.
	p := start(t, fmt.Sprintf("cd / && %q %q", binPath, fixture("code.md")))
	p.waitText("After image")
}

func TestResize(t *testing.T) {
	p := run(t, "long.md")
	p.waitText("Top Heading")
	for i := 0; i < 40; i++ {
		p.send("PageDown")
	}
	p.waitText("Bottom Marker")

	if out, err := exec.Command("tmux", "-L", p.socket, "resize-window", "-x", "50", "-y", "10").CombinedOutput(); err != nil {
		t.Skipf("resize-window unsupported: %v\n%s", err, out)
	}
	// rendering must still be alive and responsive after a resize
	for i := 0; i < 60; i++ {
		p.send("PageUp")
	}
	p.waitText("Top Heading")
	p.send("q")
	if code := p.waitExit(); code != "0" {
		t.Errorf("exit code = %q", code)
	}
}

// Observed behavior (not asserted as correct): after scrolling to the end and
// growing the window, YOffset is not re-clamped until the next key press, so
// the content stays offset and blank rows may appear below. This test only
// pins that the program survives and the next key press re-clamps.
func TestResizeGrowAtBottom(t *testing.T) {
	p := run(t, "long.md")
	p.waitText("Top Heading")
	for i := 0; i < 40; i++ {
		p.send("PageDown")
	}
	p.waitText("Bottom Marker")
	if out, err := exec.Command("tmux", "-L", p.socket, "resize-window", "-x", "80", "-y", "40").CombinedOutput(); err != nil {
		t.Skipf("resize-window unsupported: %v\n%s", err, out)
	}
	s := p.settle()
	t.Logf("after grow (no key):\n%s", s)
	p.send("j")
	s = p.settle()
	t.Logf("after grow + j:\n%s", s)
	if !strings.Contains(s, "Bottom Marker") {
		t.Errorf("bottom marker lost after grow+j:\n%s", s)
	}
}
