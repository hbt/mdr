package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/MichaelMure/go-term-markdown"
	"github.com/awesome-gocui/gocui"
	"github.com/mattn/go-isatty"
	"github.com/pkg/errors"
)

const padding = 4

// the view is placed at -YOffset, its first visible row is the buffer line below
const topRow = 1

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		printVersion()
		return
	}

	var content []byte
	var filePath string
	var loaded fileSig

	switch len(os.Args) {
	case 1:
		if isatty.IsTerminal(os.Stdin.Fd()) {
			exitError(fmt.Errorf("usage: %s <file.md>", os.Args[0]))
		}
		data, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			exitError(errors.Wrap(err, "error while reading STDIN"))
		}
		content = data
	case 2:
		// absolute, as we chdir below
		abs, err := filepath.Abs(os.Args[1])
		if err != nil {
			exitError(err)
		}
		filePath = abs
		// before the read, so a change during the read is seen as stale
		loaded, _ = statSig(filePath)
		data, err := ioutil.ReadFile(os.Args[1])
		if err != nil {
			exitError(errors.Wrap(err, "error while reading file"))
		}
		err = os.Chdir(path.Dir(os.Args[1]))
		if err != nil {
			exitError(err)
		}
		content = data

	default:
		exitError(fmt.Errorf("only one file is supported"))
	}

	g, err := gocui.NewGui(gocui.OutputNormal, false)
	if err != nil {
		exitError(errors.Wrap(err, "error starting the interactive UI"))
	}
	defer g.Close()

	ui, err := newUi(g)
	if err != nil {
		exitError(err)
	}

	ui.setContent(content)
	ui.path = filePath
	ui.loaded = loaded

	if ui.path != "" {
		done := make(chan struct{})
		defer close(done)
		go ui.watch(g, done)
	}

	if err := g.MainLoop(); err != nil && err != gocui.ErrQuit {
		exitError(err)
	}
}

func exitError(err error) {
	_, _ = fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

const (
	renderView  = "render"
	overlayView = "overlay"
)

type ui struct {
	keybindings []keybinding

	raw string
	// current width of the view
	width   int
	XOffset int
	YOffset int

	// number of lines in the rendered markdown
	lines int

	// absolute path of the displayed file, empty for stdin
	path string
	// file version currently displayed
	loaded fileSig
	// the file differs from the displayed version / could not be stat'ed
	stale, deleted bool
	// consecutive failed stats
	failures int
	// transient text for the overlay
	msg string
	// text of the line at the top before a reload, to find it again
	anchor string
}

func newUi(g *gocui.Gui) (*ui, error) {
	result := &ui{
		width: -1,
	}

	g.SetManagerFunc(result.layout)

	result.keybindings = []keybinding{
		{"", gocui.KeyCtrlC, gocui.ModNone, result.quit},
		{renderView, 'q', gocui.ModNone, result.quit},
		{renderView, 'k', gocui.ModNone, result.up},
		{renderView, gocui.KeyCtrlP, gocui.ModNone, result.up},
		{renderView, gocui.KeyArrowUp, gocui.ModNone, result.up},
		{renderView, 'j', gocui.ModNone, result.down},
		{renderView, gocui.KeyCtrlN, gocui.ModNone, result.down},
		{renderView, gocui.KeyArrowDown, gocui.ModNone, result.down},
		{renderView, gocui.KeyPgup, gocui.ModNone, result.pageUp},
		{renderView, gocui.KeyPgdn, gocui.ModNone, result.pageDown},
		{renderView, gocui.KeySpace, gocui.ModNone, result.pageDown},
		{renderView, 'R', gocui.ModNone, result.reload},
	}

	for _, kb := range result.keybindings {
		err := kb.Register(g)
		if err != nil {
			return nil, err
		}
	}

	return result, nil
}

func (ui *ui) setContent(content []byte) {
	ui.raw = string(content)
	ui.width = -1
}

func (ui *ui) layout(g *gocui.Gui) error {
	maxX, maxY := g.Size()

	v, err := g.SetView(renderView, ui.XOffset, -ui.YOffset, maxX, maxY, 0)
	if err != nil {
		if !gocui.IsUnknownView(err) {
			return err
		}

		v.Frame = false
		v.Wrap = false
	}

	if len(ui.raw) > 0 && ui.width != maxX {
		ui.width = maxX
		v.Clear()
		_, _ = v.Write(ui.render(g))
	}

	if ui.anchor != "" {
		ui.YOffset = findAnchor(v.BufferLines(), ui.anchor, ui.YOffset+topRow) - topRow
		ui.anchor = ""
	}
	ui.YOffset = clampOffset(ui.YOffset, ui.lines, maxY)
	if _, err := g.SetView(renderView, ui.XOffset, -ui.YOffset, maxX, maxY, 0); err != nil && !gocui.IsUnknownView(err) {
		return err
	}

	if err := ui.layoutOverlay(g); err != nil {
		return err
	}

	_, err = g.SetCurrentView(renderView)
	if err != nil {
		return err
	}

	return nil
}

// layoutOverlay shows the bottom-right indicator, or removes it.
func (ui *ui) layoutOverlay(g *gocui.Gui) error {
	text := overlayText(ui.stale, ui.deleted, ui.msg)
	if text == "" {
		err := g.DeleteView(overlayView)
		if err != nil && !gocui.IsUnknownView(err) {
			return err
		}
		return nil
	}

	maxX, maxY := g.Size()
	v, err := g.SetView(overlayView, max(maxX-len(text)-1, 0), maxY-2, maxX, maxY, 0)
	if err != nil {
		if !gocui.IsUnknownView(err) {
			return err
		}

		v.Frame = false
		v.Wrap = false
		v.BgColor = gocui.ColorYellow
		v.FgColor = gocui.ColorBlack
	}
	v.Clear()
	_, _ = fmt.Fprint(v, text)
	_, err = g.SetViewOnTop(overlayView)
	return err
}

// watch polls the file and flags the ui as stale when it changes on disk.
func (ui *ui) watch(g *gocui.Gui, done <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		sig, err := statSig(ui.path)
		g.Update(func(g *gocui.Gui) error {
			ui.observe(sig, err)
			return nil
		})
	}
}

// observe records the result of a poll, in the main loop.
func (ui *ui) observe(sig fileSig, err error) {
	if err != nil {
		// a save can briefly remove the file, wait for a second failure
		ui.failures++
		if ui.failures >= 2 {
			ui.stale, ui.deleted = true, true
		}
		return
	}
	ui.failures = 0
	ui.deleted = false
	ui.stale = isStale(ui.loaded, sig, nil)
}

func (ui *ui) reload(g *gocui.Gui, v *gocui.View) error {
	ui.msg = ""
	if ui.path == "" {
		ui.msg = "stdin: cannot reload"
		return nil
	}

	// before the read, so a change during the read is seen as stale
	sig, _ := statSig(ui.path)
	data, err := ioutil.ReadFile(ui.path)
	if err != nil {
		if pe, ok := err.(*os.PathError); ok {
			err = pe.Err
		}
		ui.msg = "reload failed: " + err.Error()
		return nil
	}

	ui.anchor = anchorLine(v.BufferLines(), ui.YOffset+topRow)
	ui.setContent(data)
	ui.loaded = sig
	ui.stale, ui.deleted, ui.failures = false, false, 0
	return nil
}

func (ui *ui) render(g *gocui.Gui) []byte {
	maxX, _ := g.Size()

	opts := []markdown.Options{
		// needed when going through gocui
		markdown.WithImageDithering(markdown.DitheringWithBlocks),
	}

	rendered := markdown.Render(ui.raw, maxX-1-padding, padding, opts...)
	ui.lines = 0
	for _, b := range rendered {
		if b == '\n' {
			ui.lines++
		}
	}
	return rendered
}

func (ui *ui) quit(g *gocui.Gui, v *gocui.View) error {
	return gocui.ErrQuit
}

func (ui *ui) up(g *gocui.Gui, v *gocui.View) error {
	ui.msg = ""
	ui.YOffset -= 1
	ui.YOffset = max(ui.YOffset, 0)
	return nil
}

func (ui *ui) down(g *gocui.Gui, v *gocui.View) error {
	ui.msg = ""
	_, maxY := g.Size()
	ui.YOffset += 1
	ui.YOffset = min(ui.YOffset, ui.lines-maxY+1)
	ui.YOffset = max(ui.YOffset, 0)
	return nil
}

func (ui *ui) pageUp(g *gocui.Gui, v *gocui.View) error {
	ui.msg = ""
	_, maxY := g.Size()
	ui.YOffset -= maxY / 2
	ui.YOffset = max(ui.YOffset, 0)
	return nil
}

func (ui *ui) pageDown(g *gocui.Gui, v *gocui.View) error {
	ui.msg = ""
	_, maxY := g.Size()
	ui.YOffset += maxY / 2
	ui.YOffset = min(ui.YOffset, ui.lines-maxY+1)
	ui.YOffset = max(ui.YOffset, 0)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
