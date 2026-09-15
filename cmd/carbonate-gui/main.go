//go:build gtk

// Command carbonate-gui is a small window for setting up and running the
// bridge, for people who would rather not keep a terminal open.
//
// It runs exactly the same server as `carbonate serve`, from the same
// internal package, so the two cannot drift apart.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const appID = "io.github.develonrails.Carbonate"

func main() {
	app := gtk.NewApplication(appID, gio.ApplicationFlagsNone)

	app.ConnectActivate(func() { newUI(app).show() })

	if code := app.Run(os.Args); code > 0 {
		os.Exit(code)
	}
}

// ui holds the widgets that change as the bridge moves between states.
type ui struct {
	app    *gtk.Application
	window *gtk.ApplicationWindow
	stack  *gtk.Stack

	status    *gtk.Label
	bridgeRow *gtk.Box
	bridge    *bridge
}

// showBridgePassword puts the password where the user can read and copy it,
// since every client they configure will ask for it.
func (u *ui) showBridgePassword(password string) {
	for {
		child := u.bridgeRow.FirstChild()
		if child == nil {
			break
		}

		u.bridgeRow.Remove(child)
	}

	name := gtk.NewLabel("Bridge password")
	name.SetXAlign(0)

	value := selectable(password)
	value.AddCSSClass("monospace")

	u.bridgeRow.Append(name)
	u.bridgeRow.Append(value)
}

func newUI(app *gtk.Application) *ui {
	u := &ui{app: app, bridge: newBridge()}

	u.window = gtk.NewApplicationWindow(app)
	u.window.SetTitle("Carbonate")
	u.window.SetDefaultSize(460, 380)

	u.stack = gtk.NewStack()
	u.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)

	u.status = gtk.NewLabel("")
	u.status.SetWrap(true)
	u.status.SetXAlign(0)

	outer := gtk.NewBox(gtk.OrientationVertical, 12)
	outer.SetMarginTop(18)
	outer.SetMarginBottom(18)
	outer.SetMarginStart(18)
	outer.SetMarginEnd(18)
	outer.Append(u.stack)
	outer.Append(u.status)

	u.stack.AddNamed(u.loginPage(), "login")
	u.stack.AddNamed(u.unlockPage(), "unlock")
	u.stack.AddNamed(u.runPage(), "run")

	u.window.SetChild(outer)

	// A stored session only needs unlocking; a fresh one needs a Proton login.
	if u.bridge.hasSession() {
		u.stack.SetVisibleChildName("unlock")
	} else {
		u.stack.SetVisibleChildName("login")
	}

	return u
}

func (u *ui) show() {
	u.window.SetVisible(true)
}

// say reports progress or failure, from any goroutine.
func (u *ui) say(format string, args ...any) {
	message := fmt.Sprintf(format, args...)

	glib.IdleAdd(func() { u.status.SetText(message) })
}

func heading(text string) *gtk.Label {
	label := gtk.NewLabel(text)
	label.SetXAlign(0)
	label.AddCSSClass("title-4")

	return label
}

func field(label string, entry gtk.Widgetter) *gtk.Box {
	box := gtk.NewBox(gtk.OrientationVertical, 4)

	name := gtk.NewLabel(label)
	name.SetXAlign(0)

	box.Append(name)
	box.Append(entry)

	return box
}

// loginPage signs in to Proton for the first time.
func (u *ui) loginPage() *gtk.Box {
	page := gtk.NewBox(gtk.OrientationVertical, 12)

	username := gtk.NewEntry()
	username.SetPlaceholderText("you@proton.me")

	password := gtk.NewPasswordEntry()
	password.SetShowPeekIcon(true)

	twoFactor := gtk.NewEntry()
	twoFactor.SetPlaceholderText("Leave empty if you have no second factor")

	// Two-password accounts keep the key that unlocks your data separate from
	// the one that proves who you are. Most accounts use one for both.
	mailbox := gtk.NewPasswordEntry()
	mailbox.SetShowPeekIcon(true)

	// Only needed when Proton asks for proof that a person is present; the
	// link to answer it appears below when that happens.
	verification := gtk.NewEntry()
	verification.SetPlaceholderText("Only if asked for below")

	button := gtk.NewButtonWithLabel("Log in")
	button.AddCSSClass("suggested-action")

	page.Append(heading("Connect to Proton"))
	page.Append(field("Proton address", username))
	page.Append(field("Password", password))
	page.Append(field("Two-factor code", twoFactor))
	page.Append(field("Mailbox password (only if separate from the one above)", mailbox))
	page.Append(field("Verification token", verification))
	page.Append(button)

	button.ConnectClicked(func() {
		user := username.Text()
		pass := password.Text()
		code := twoFactor.Text()
		mailboxPass := mailbox.Text()
		token := verification.Text()

		if user == "" || pass == "" {
			u.say("Fill in your Proton address and password.")

			return
		}

		button.SetSensitive(false)
		u.say("Signing in…")

		go func() {
			defer glib.IdleAdd(func() { button.SetSensitive(true) })

			bridgePassword, err := u.bridge.login(context.Background(), user, []byte(pass), code, mailboxPass, token,
				func(message string) { u.say("%s", message) })
			if err != nil {
				u.say("Login failed: %v", err)

				return
			}

			glib.IdleAdd(func() {
				u.showBridgePassword(bridgePassword)
				u.stack.SetVisibleChildName("run")
			})

			u.say("Signed in. The bridge password is shown above — your calendar and contacts apps need it.")
		}()
	})

	return page
}

// unlockPage opens a session that already exists.
func (u *ui) unlockPage() *gtk.Box {
	page := gtk.NewBox(gtk.OrientationVertical, 12)

	password := gtk.NewPasswordEntry()
	password.SetShowPeekIcon(true)

	unlock := gtk.NewButtonWithLabel("Unlock")
	unlock.AddCSSClass("suggested-action")

	forget := gtk.NewButtonWithLabel("Sign in as someone else")

	page.Append(heading("Unlock the bridge"))
	page.Append(field("Bridge password", password))
	page.Append(unlock)
	page.Append(forget)

	open := func() {
		entered := password.Text()
		if entered == "" {
			u.say("Enter the bridge password you were given when you signed in.")

			return
		}

		unlock.SetSensitive(false)
		u.say("Unlocking…")

		go func() {
			defer glib.IdleAdd(func() { unlock.SetSensitive(true) })

			if err := u.bridge.unlock(context.Background(), entered); err != nil {
				u.say("Could not unlock: %v", err)

				return
			}

			glib.IdleAdd(func() {
				u.showBridgePassword(entered)
				u.stack.SetVisibleChildName("run")
			})

			u.say("Unlocked.")
		}()
	}

	unlock.ConnectClicked(open)
	password.ConnectActivate(open)

	forget.ConnectClicked(func() {
		u.stack.SetVisibleChildName("login")
		u.say("")
	})

	return page
}

// runPage starts and stops the server and shows what to type into a client.
func (u *ui) runPage() *gtk.Box {
	page := gtk.NewBox(gtk.OrientationVertical, 12)

	address := gtk.NewEntry()
	address.SetText("127.0.0.1:8080")

	toggle := gtk.NewButtonWithLabel("Start")
	toggle.AddCSSClass("suggested-action")

	u.bridgeRow = gtk.NewBox(gtk.OrientationVertical, 4)

	calendarURL := selectable("")
	contactsURL := selectable("")

	page.Append(heading("Serve"))
	page.Append(field("Listen on", address))
	page.Append(u.bridgeRow)
	page.Append(toggle)
	page.Append(field("Calendar address", calendarURL))
	page.Append(field("Contacts address", contactsURL))

	setURLs := func(addr string) {
		calendarURL.SetText("http://" + addr + "/caldav/")
		contactsURL.SetText("http://" + addr + "/carddav/")
	}

	toggle.ConnectClicked(func() {
		if u.bridge.running() {
			u.bridge.stop()
			toggle.SetLabel("Start")
			address.SetSensitive(true)
			u.say("Stopped.")

			return
		}

		addr := address.Text()

		toggle.SetSensitive(false)

		go func() {
			defer glib.IdleAdd(func() { toggle.SetSensitive(true) })

			if err := u.bridge.start(addr); err != nil {
				u.say("Could not start: %v", err)

				return
			}

			glib.IdleAdd(func() {
				toggle.SetLabel("Stop")
				address.SetSensitive(false)
				setURLs(addr)
			})

			u.say("Serving as %s. Sign in to your apps with that address and the bridge password.", u.bridge.account())
		}()
	})

	return page
}

func selectable(text string) *gtk.Label {
	label := gtk.NewLabel(text)
	label.SetXAlign(0)
	label.SetSelectable(true)

	return label
}
