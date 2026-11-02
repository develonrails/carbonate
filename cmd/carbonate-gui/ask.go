//go:build gtk

package main

import (
	"errors"
	"strings"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// The window is what a login asks when it needs a person.
var _ asker = (*ui)(nil)

// errCancelled is a person choosing not to go on. It ends the login like any
// failure, but is not one to report back to them.
var errCancelled = errors.New("cancelled")

// question is something a login needs from the person before it can go on.
type question struct {
	heading string
	body    string

	// link, if set, is the page the answer is found on, behind a button.
	link string

	// entry, if set, titles the field the answer is typed into. Without one,
	// the button alone is the answer.
	entry  string
	digits bool

	// accept labels the button that answers.
	accept string
}

// ask puts a question in a dialog and waits for the answer.
//
// It blocks, so it is for the goroutine doing the work and never for GTK's own
// thread, which has to stay free to draw the dialog it is waiting on.
func (u *ui) ask(q question) (string, error) {
	type reply struct {
		text string
		ok   bool
	}

	replies := make(chan reply, 1)

	glib.IdleAdd(func() {
		dialog := adw.NewAlertDialog(q.heading, q.body)
		dialog.AddResponse("cancel", "Cancel")
		dialog.AddResponse("accept", q.accept)
		dialog.SetResponseAppearance("accept", adw.ResponseSuggested)
		dialog.SetDefaultResponse("accept")
		dialog.SetCloseResponse("cancel")

		extra := gtk.NewBox(gtk.OrientationVertical, 12)

		if q.link != "" {
			// A link button opens the page through the portal, and its menu
			// copies the address, for a browser somewhere else.
			open := gtk.NewLinkButtonWithLabel(q.link, "Open the verification page")
			extra.Append(open)
		}

		var field *adw.EntryRow

		if q.entry != "" {
			field = adw.NewEntryRow()
			field.SetTitle(q.entry)
			field.SetActivatesDefault(true)

			if q.digits {
				field.SetInputPurpose(gtk.InputPurposeDigits)
			}

			// Answering with nothing would only fail one step later, and
			// further from the field that was left empty.
			dialog.SetResponseEnabled("accept", false)
			field.ConnectChanged(func() {
				dialog.SetResponseEnabled("accept", strings.TrimSpace(field.Text()) != "")
			})

			list := gtk.NewListBox()
			list.AddCSSClass("boxed-list")
			list.SetSelectionMode(gtk.SelectionNone)
			list.Append(field)
			extra.Append(list)
		}

		dialog.SetExtraChild(extra)

		dialog.ConnectResponse(func(response string) {
			r := reply{ok: response == "accept"}
			if r.ok && field != nil {
				r.text = strings.TrimSpace(field.Text())
			}

			// Never block GTK's thread on a reader that has already had
			// its answer.
			select {
			case replies <- r:
			default:
			}
		})

		dialog.Present(u.window)

		if field != nil {
			dialog.SetFocus(field)
		}
	})

	r := <-replies
	if !r.ok {
		return "", errCancelled
	}

	return r.text, nil
}

func (u *ui) confirmCaptcha(link string) error {
	_, err := u.ask(question{
		heading: "Show Proton you are a person",
		body: "Proton wants a puzzle solved before it lets you sign in. " +
			"Open the page, solve it, and come back once it says verification is complete. " +
			"There is no code to copy.",
		link:   link,
		accept: "Continue",
	})

	return err
}

func (u *ui) verificationToken(link string) (string, error) {
	return u.ask(question{
		heading: "Show Proton you are a person",
		body:    "Proton wants a check before it lets you sign in. Open the page, do what it asks, and paste the token it gives you.",
		link:    link,
		entry:   "Verification token",
		accept:  "Continue",
	})
}

func (u *ui) twoFactorCode(stale bool) (string, error) {
	body := "Enter the code your authenticator app shows for Proton."
	if stale {
		body = "The code you typed has probably expired while you solved the puzzle. Enter the one your authenticator app shows now."
	}

	return u.ask(question{
		heading: "Two-factor code",
		body:    body,
		entry:   "Code",
		digits:  true,
		accept:  "Sign in",
	})
}

// fail reports what went wrong, in full.
//
// Not a toast: a toast has one line, and an error is most useful in exactly
// the part a line cuts off. A dialog also stays until it has been read.
func (u *ui) fail(heading string, err error) {
	message := err.Error()

	glib.IdleAdd(func() {
		dialog := adw.NewAlertDialog(heading, message)
		dialog.AddResponse("close", "Close")
		dialog.SetDefaultResponse("close")
		dialog.SetCloseResponse("close")
		dialog.Present(u.window)
	})
}
