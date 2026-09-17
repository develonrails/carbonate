//go:build gtk

package main

import (
	"strings"
	"sync"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// activityLines bounds what the window keeps. A bridge left running for a week
// would otherwise grow a log nobody reads until it is too large to read.
const activityLines = 500

// activity is the server's log, shown in the window.
//
// The command line has `carbonate serve -log`; someone running the window has
// no terminal to read, and "my calendar will not sync" is exactly the report
// that cannot be answered without one. It is the same stream, written to a
// text view instead of standard output.
//
// Every widget here is built once and kept. A widget may have one parent, so
// handing the same view to a second scrolled window — which is what building
// these per visit did — leaves the second one empty and GTK complaining.
type activity struct {
	view   *gtk.TextView
	buffer *gtk.TextBuffer
	stack  *gtk.Stack

	// Two kinds of line, told apart at a glance: what Proton sent, and what
	// went wrong. Everything else is a client polling, which is the bulk of
	// it and the least interesting.
	fromProton *gtk.TextTag
	failure    *gtk.TextTag

	// pending collects lines arriving between turns of the main loop, so a
	// burst of requests costs one update rather than one per line.
	mu      sync.Mutex
	pending []string
	queued  bool
	shown   bool
}

func newActivity() *activity {
	view := gtk.NewTextView()
	view.SetEditable(false)
	view.SetCursorVisible(false)
	view.SetMonospace(true)
	view.SetWrapMode(gtk.WrapWordChar)
	view.SetLeftMargin(12)
	view.SetRightMargin(12)
	view.SetTopMargin(12)
	view.SetBottomMargin(12)
	view.SetPixelsAboveLines(2)

	a := &activity{view: view, buffer: view.Buffer()}

	a.fromProton = gtk.NewTextTag("proton")
	a.fromProton.SetObjectProperty("weight", 700)
	a.buffer.TagTable().Add(a.fromProton)

	a.failure = gtk.NewTextTag("failure")
	a.failure.SetObjectProperty("weight", 700)
	// Adwaita's red, which stays legible on both the light and dark palette.
	a.failure.SetObjectProperty("foreground", "#e01b24")
	a.buffer.TagTable().Add(a.failure)

	scroll := gtk.NewScrolledWindow()
	scroll.SetChild(view)
	scroll.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyAutomatic)
	scroll.SetVExpand(true)
	scroll.SetHExpand(true)

	// A card, so the log reads as a panel of its own rather than as text
	// spilled onto the window.
	frame := gtk.NewFrame("")
	frame.SetChild(scroll)
	frame.SetMarginTop(12)
	frame.SetMarginBottom(12)
	frame.SetMarginStart(12)
	frame.SetMarginEnd(12)
	frame.AddCSSClass("view")

	// Before the bridge starts there is nothing to show, and an empty box
	// invites the conclusion that something is broken.
	empty := adw.NewStatusPage()
	empty.SetIconName("utilities-system-monitor-symbolic")
	empty.SetTitle("Nothing yet")
	empty.SetDescription(
		"Start the bridge, and this shows what your apps ask for and what Proton sends back.")

	a.stack = gtk.NewStack()
	a.stack.AddNamed(empty, "empty")
	a.stack.AddNamed(frame, "log")
	a.stack.SetVisibleChildName("empty")

	return a
}

// Write accepts the server's log from whichever goroutine produced it.
//
// GTK may only be touched from the main loop, and these arrive on the
// goroutines serving DAV requests, so the text crosses over through IdleAdd.
func (a *activity) Write(p []byte) (int, error) {
	// p belongs to the caller once this returns, so it has to be copied.
	text := string(p)

	a.mu.Lock()
	a.pending = append(a.pending, text)

	queue := !a.queued
	a.queued = true
	a.mu.Unlock()

	if queue {
		glib.IdleAdd(a.flush)
	}

	return len(p), nil
}

// flush appends what has arrived and scrolls to it. Runs on the main loop.
func (a *activity) flush() {
	a.mu.Lock()
	text := strings.Join(a.pending, "")
	a.pending = nil
	a.queued = false
	shown := a.shown
	a.shown = true
	a.mu.Unlock()

	if text == "" {
		return
	}

	if !shown {
		a.stack.SetVisibleChildName("log")
	}

	for _, line := range strings.SplitAfter(text, "\n") {
		if line == "" {
			continue
		}

		a.appendLine(line)
	}

	a.trim()

	// Keep the newest line in view, which is the one worth reading.
	a.view.ScrollToIter(a.buffer.EndIter(), 0, false, 0, 0)
}

// appendLine writes one line, marked for what it is.
func (a *activity) appendLine(line string) {
	from := a.buffer.EndIter().Offset()

	a.buffer.Insert(a.buffer.EndIter(), line)

	tag := a.tagFor(line)
	if tag == nil {
		return
	}

	a.buffer.ApplyTag(tag, a.buffer.IterAtOffset(from), a.buffer.EndIter())
}

// tagFor decides how a line should read, or nil to leave it plain.
func (a *activity) tagFor(line string) *gtk.TextTag {
	if failed(line) {
		return a.failure
	}

	if strings.HasPrefix(line, "carbonate:") {
		return a.fromProton
	}

	return nil
}

// failed reports whether a request line carries a status that is not success.
//
// The status is the fourth field of a request line: time, method, path, status.
func failed(line string) bool {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return strings.Contains(line, "failed")
	}

	switch code := fields[3]; {
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "5"):
		return len(code) == 3
	}

	return false
}

// trim drops the oldest lines once there are more than anyone will scroll back
// through.
func (a *activity) trim() {
	excess := a.buffer.LineCount() - activityLines
	if excess <= 0 {
		return
	}

	cut, ok := a.buffer.IterAtLine(excess)
	if !ok {
		return
	}

	a.buffer.Delete(a.buffer.StartIter(), cut)
}

// text returns everything shown, for copying into a bug report.
func (a *activity) text() string {
	return a.buffer.Text(a.buffer.StartIter(), a.buffer.EndIter(), false)
}

// widget returns the log's one and only widget.
func (a *activity) widget() gtk.Widgetter {
	return a.stack
}
