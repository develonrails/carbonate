//go:build gtk

// Command carbonate-gui is a small window for setting up and running the
// bridge, for people who would rather not keep a terminal open.
//
// It runs exactly the same server as `carbonate serve`, from the same
// internal package, so the two cannot drift apart.
//
// The window is built on libadwaita rather than plain GTK, which is what
// makes it navigable: AdwNavigationView gives every page a back button, an
// Escape key and a swipe, so no screen is one a person can get stuck on.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/diamondburned/gotk4-adwaita/pkg/adw"
	"github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	glibv2 "github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/develonrails/carbonate/internal/session"
)

const appID = "io.github.develonrails.Carbonate"

func main() {
	background, args := takeBackgroundFlag(os.Args)

	app := adw.NewApplication(appID, gio.ApplicationFlagsNone)

	// Activating again — from the launcher, or by starting a second copy —
	// must raise the window that is already there. Building a second would
	// mean two bridges fighting over one session.
	var window *ui

	app.ConnectActivate(func() {
		if window == nil {
			window = newUI(app, background)
		}

		// Started at login: serve, and stay off the screen. GApplication
		// would otherwise quit an application with no window, so it is held
		// open explicitly.
		if window.hidden {
			app.Hold()

			return
		}

		window.show()
	})

	if code := app.Run(args); code > 0 {
		os.Exit(code)
	}
}

// backgroundFlag asks for a start with no window, and is what the autostart
// entry carries.
const backgroundFlag = "--background"

// takeBackgroundFlag removes the flag before GApplication sees it, since it
// parses what is left and refuses what it does not know.
func takeBackgroundFlag(argv []string) (bool, []string) {
	background := false
	rest := make([]string, 0, len(argv))

	for _, arg := range argv {
		if arg == backgroundFlag {
			background = true

			continue
		}

		rest = append(rest, arg)
	}

	return background, rest
}

// watchChoices are how often carbonate may ask Proton whether anything has
// changed, without waiting for a client to ask.
//
// This cannot make a calendar app notice a change any sooner — CalDAV has no
// way to tell one to come early, and the interval it polls on is its own. It
// decides how quickly a change shows up *in the log*, which is what turns
// "nothing is arriving from Proton" into something you can watch happen.
var watchChoices = []struct {
	label string
	every time.Duration
}{
	{"Only when an app asks", 0},
	{"Every 30 seconds", 30 * time.Second},
	{"Every minute", time.Minute},
	{"Every 5 minutes", 5 * time.Minute},
	{"Every 15 minutes", 15 * time.Minute},
	{"Every 30 minutes", 30 * time.Minute},
	{"Every hour", time.Hour},
}

// defaultWatch is often enough to watch a change arrive while you wait for it,
// and rare enough to leave running. Named rather than an index into the list
// above, which would move the next time a choice is added.
const defaultWatch = time.Minute

func watchDefault() uint {
	for i, c := range watchChoices {
		if c.every == defaultWatch {
			return uint(i)
		}
	}

	return 0
}

// ui holds the widgets that change as the bridge moves between states.
type ui struct {
	app    *adw.Application
	window *adw.ApplicationWindow
	toasts *adw.ToastOverlay
	pages  *adw.NavigationView

	bridge   *bridge
	activity *activity
	prefs    prefs

	// Built once and pushed again on each visit. A NavigationPage owns its
	// child, so building a second one around the same log would leave the
	// log with two parents — which GTK refuses, silently and emptily.
	activityNav *adw.NavigationPage

	// Rebuilt when the bridge starts, so the serve page can show what a
	// client needs without being reconstructed.
	// hidden is set when carbonate was started at login, and cleared the
	// moment anything needs a person.
	hidden bool

	bridgePassword *adw.ActionRow
	calendarRow    *adw.ActionRow
	contactsRow    *adw.ActionRow
	watch          *adw.ComboRow
}

func newUI(app *adw.Application, background bool) *ui {
	u := &ui{app: app, bridge: newBridge(), activity: newActivity(), prefs: loadPrefs(), hidden: background}

	u.window = adw.NewApplicationWindow(&app.Application)
	u.window.SetTitle("Carbonate")
	u.window.SetDefaultSize(600, 760)

	// The icon is found by name, from the theme, under the application ID.
	// Wayland matches it to the .desktop file; X11 needs to be told.
	u.window.SetIconName(appID)

	u.installActions()

	// Closing the window is not always quitting. Someone who asked for the
	// bridge to keep serving means the button to mean "put it away", and a
	// hidden window keeps the application alive on its own.
	u.window.ConnectCloseRequest(func() bool {
		if !u.prefs.RunInBackground || !u.bridge.running() {
			return false
		}

		u.window.SetVisible(false)

		return true
	})

	u.pages = adw.NewNavigationView()

	u.toasts = adw.NewToastOverlay()
	u.toasts.SetChild(u.pages)

	u.window.SetContent(u.toasts)

	// A stored session only needs unlocking; a fresh one needs a Proton login.
	if u.bridge.hasSession() {
		u.pages.Push(u.unlockPage())
		u.unlockFromKeyring()
	} else {
		u.pages.Push(u.loginPage())

		// Nothing to start from, so there is nothing to be quiet about.
		u.reveal()
	}

	return u
}

// reveal puts the window on screen, and stops it being kept off.
//
// Starting at login is only worth doing silently while it works. An
// application holding itself open with no window and nothing serving is one
// nobody can find, so every path that cannot serve ends here.
func (u *ui) reveal() {
	if !u.hidden {
		return
	}

	u.hidden = false

	u.window.Present()
}

// unlockFromKeyring opens the session with the remembered password and starts
// serving, without anybody being asked anything.
//
// This is what makes starting at login mean something. A bridge that comes up
// and waits on a password has not started: the calendar apps that were the
// reason for starting it find nothing listening, and nobody is at the screen
// to notice.
//
// Every failure here is quiet and falls back to the unlock page that is
// already showing. A machine with no keyring, a locked one, or a password
// that no longer fits are all reasons to ask rather than to complain.
func (u *ui) unlockFromKeyring() {
	password, found, err := session.Recall(u.bridge.sessionPath())
	if err != nil || !found {
		u.reveal()

		return
	}

	go func() {
		if err := u.bridge.unlock(context.Background(), password); err != nil {
			glib.IdleAdd(func() { u.reveal() })

			return
		}

		glib.IdleAdd(func() {
			u.showServing(u.servePage(password, true))
		})
	}()
}

// remember keeps the bridge password for next time, and says so when it
// cannot — silently forgetting would mean the next login is a surprise.
func (u *ui) remember(bridgePassword string) {
	if err := session.Remember(u.bridge.sessionPath(), bridgePassword); err != nil {
		u.say("Could not save the bridge password for next time: %v", err)
	}
}

// showServing puts the serve page up in place of whatever led to it.
//
// Unlocking and signing in are gates, not destinations: once through, there
// is nothing to go back to. Pushing left a back button that returned to a
// screen asking for a password already given, while the bridge carried on
// serving behind it — and no way forward again without unlocking twice.
func (u *ui) showServing(page *adw.NavigationPage) {
	u.pages.Replace([]*adw.NavigationPage{page})
}

func (u *ui) show() {
	u.window.Present()
}

// say reports progress or failure, from any goroutine.
//
// A toast rather than a label: it is the thing that just happened, and it
// should not sit on screen afterwards pretending to describe the present.
func (u *ui) say(format string, args ...any) {
	message := fmt.Sprintf(format, args...)

	glib.IdleAdd(func() { u.toasts.AddToast(adw.NewToast(message)) })
}

// page wraps content in a navigable page with a header bar and the menu.
func (u *ui) page(title string, content gtk.Widgetter) *adw.NavigationPage {
	header := adw.NewHeaderBar()
	header.PackEnd(u.menuButton())

	view := adw.NewToolbarView()
	view.AddTopBar(header)
	view.SetContent(content)

	return adw.NewNavigationPage(view, title)
}

// menuButton is the primary menu every GNOME app has in the same corner.
func (u *ui) menuButton() *gtk.MenuButton {
	menu := gio.NewMenu()
	menu.Append("Preferences", "app.preferences")
	menu.Append("Quit", "app.quit")

	button := gtk.NewMenuButton()
	button.SetIconName("open-menu-symbolic")
	button.SetTooltipText("Main menu")
	button.SetMenuModel(menu)

	return button
}

// installActions wires the menu to something, and gives the shortcuts people
// already have in their fingers somewhere to go.
func (u *ui) installActions() {
	preferences := gio.NewSimpleAction("preferences", nil)
	preferences.ConnectActivate(func(*glibv2.Variant) { u.showPreferences() })
	u.app.AddAction(preferences)

	quit := gio.NewSimpleAction("quit", nil)
	quit.ConnectActivate(func(*glibv2.Variant) {
		// Stop serving before going, rather than leaving the session locked
		// by a process that is on its way out.
		u.bridge.stop()
		u.app.Quit()
	})
	u.app.AddAction(quit)

	u.app.SetAccelsForAction("app.quit", []string{"<Primary>q"})
	u.app.SetAccelsForAction("app.preferences", []string{"<Primary>comma"})
}

// showPreferences offers the two choices that outlive a session: whether
// carbonate starts with the computer, and whether closing the window stops it.
func (u *ui) showPreferences() {
	dialog := adw.NewPreferencesDialog()
	dialog.SetTitle("Preferences")

	prefsPage := adw.NewPreferencesPage()

	group := adw.NewPreferencesGroup()
	group.SetTitle("Running")

	// The switch shows what was last asked for, not what is currently set:
	// the portal cannot be asked. The system owns that answer and shows it in
	// its own settings, which the subtitle points at rather than pretending
	// we know better.
	login := adw.NewSwitchRow()
	login.SetTitle("Start at login")
	login.SetSubtitle("Your system asks before allowing this, and can withdraw it later in its background app settings")

	background := adw.NewSwitchRow()
	background.SetTitle("Keep running when the window is closed")
	background.SetSubtitle("Your calendar apps keep working. Quit from this menu to stop it.")
	background.SetActive(u.prefs.RunInBackground)

	group.Add(login)
	group.Add(background)
	prefsPage.Add(group)
	dialog.Add(prefsPage)

	login.ConnectAfter("notify::active", func() {
		want := login.Active()

		login.SetSensitive(false)

		requestAutostart(context.Background(), want, func(granted bool, err error) {
			glib.IdleAdd(func() {
				login.SetSensitive(true)

				switch {
				case err != nil:
					u.say("Could not ask to start at login: %v", err)
				case !granted:
					u.say("Your system did not allow starting at login.")
				case want:
					u.say("Carbonate will start at login.")
				default:
					u.say("Carbonate will no longer start at login.")
				}

				// The switch follows the answer, not the click, so a refusal
				// leaves it showing what is actually true.
				if got := granted && want; got != login.Active() {
					login.SetActive(got)
				}
			})
		})
	})

	background.ConnectAfter("notify::active", func() {
		u.prefs.RunInBackground = background.Active()

		if err := u.prefs.save(); err != nil {
			u.say("That setting will not survive a restart: %v", err)
		}
	})

	dialog.Present(u.window)
}

// scrolled lets a page be smaller than its contents rather than forcing the
// window to grow past the screen.
func scrolled(child gtk.Widgetter) *gtk.ScrolledWindow {
	s := gtk.NewScrolledWindow()
	s.SetChild(child)
	s.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	s.SetVExpand(true)

	return s
}

func primary(label string) *gtk.Button {
	b := gtk.NewButtonWithLabel(label)
	b.AddCSSClass("suggested-action")
	b.AddCSSClass("pill")
	b.SetHAlign(gtk.AlignCenter)
	b.SetMarginTop(12)

	return b
}

// loginPage signs in to Proton for the first time.
//
// Only the two things everyone has are shown; a second factor, a separate
// mailbox password and a human-verification token are folded away, because
// most accounts have none of them and a form of five secrets reads as five
// things you are expected to know.
func (u *ui) loginPage() *adw.NavigationPage {
	prefs := adw.NewPreferencesPage()

	account := adw.NewPreferencesGroup()
	account.SetTitle("Proton account")
	account.SetDescription("Your Proton password is used once, to sign in. It is not stored.")

	username := adw.NewEntryRow()
	username.SetTitle("Proton address")

	password := adw.NewPasswordEntryRow()
	password.SetTitle("Password")

	account.Add(username)
	account.Add(password)

	extras := adw.NewPreferencesGroup()

	more := adw.NewExpanderRow()
	more.SetTitle("If your account needs more")
	more.SetSubtitle("Two-factor code, separate mailbox password, verification")

	twoFactor := adw.NewEntryRow()
	twoFactor.SetTitle("Two-factor code")

	mailbox := adw.NewPasswordEntryRow()
	mailbox.SetTitle("Mailbox password, if separate")

	verification := adw.NewEntryRow()
	verification.SetTitle("Verification token, only if asked for")

	more.AddRow(twoFactor)
	more.AddRow(mailbox)
	more.AddRow(verification)
	extras.Add(more)

	button := primary("Sign in")

	box := gtk.NewBox(gtk.OrientationVertical, 0)
	box.Append(prefs)
	prefs.Add(account)
	prefs.Add(extras)

	buttons := adw.NewPreferencesGroup()
	buttons.Add(button)
	prefs.Add(buttons)

	button.ConnectClicked(func() {
		user, pass := username.Text(), password.Text()

		if user == "" || pass == "" {
			u.say("Fill in your Proton address and password.")

			return
		}

		button.SetSensitive(false)
		u.say("Signing in…")

		go func() {
			defer glib.IdleAdd(func() { button.SetSensitive(true) })

			bridgePassword, err := u.bridge.login(context.Background(), user, []byte(pass),
				twoFactor.Text(), mailbox.Text(), verification.Text(),
				func(message string) { u.say("%s", message) })
			if err != nil {
				u.say("Sign-in failed: %v", err)

				return
			}

			u.remember(bridgePassword)

			glib.IdleAdd(func() {
				u.showServing(u.servePage(bridgePassword, false))
			})
		}()
	})

	return u.page("Carbonate", scrolled(box))
}

// unlockPage opens a session that already exists.
func (u *ui) unlockPage() *adw.NavigationPage {
	prefs := adw.NewPreferencesPage()

	group := adw.NewPreferencesGroup()
	group.SetTitle("Unlock")
	group.SetDescription("The bridge password is the one Carbonate gave you when you signed in — not your Proton password.")

	password := adw.NewPasswordEntryRow()
	password.SetTitle("Bridge password")
	group.Add(password)

	unlock := primary("Unlock")

	buttons := adw.NewPreferencesGroup()
	buttons.Add(unlock)

	// The way out, for the person who has lost the password this page is
	// asking for. Filed under the other account it is also for, it reads as
	// something for somebody else, and they stay stuck.
	other := adw.NewPreferencesGroup()
	other.SetTitle("Lost it, or signing in as someone else?")

	forget := adw.NewActionRow()
	forget.SetTitle("Sign in to Proton again")
	forget.SetSubtitle("Gets you a new bridge password. Your calendar apps will need the new one.")
	forget.SetActivatable(true)
	forget.AddSuffix(gtk.NewImageFromIconName("go-next-symbolic"))
	other.Add(forget)

	prefs.Add(group)
	prefs.Add(buttons)
	prefs.Add(other)

	open := func() {
		entered := password.Text()
		if entered == "" {
			u.say("Enter the bridge password Carbonate gave you.")

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

			u.remember(entered)

			glib.IdleAdd(func() { u.showServing(u.servePage(entered, false)) })
		}()
	}

	unlock.ConnectClicked(open)
	password.ConnectEntryActivated(open)

	// Signing in as someone else throws away a session that cannot be got
	// back without the Proton password, and mints a bridge password that
	// every configured client will then reject. Saying so first is the whole
	// point of asking.
	forget.ConnectActivated(func() {
		dialog := adw.NewAlertDialog("Sign in to Proton again?",
			"This forgets the Proton session stored on this computer.\n\n"+
				"You will need your Proton password again, and Carbonate will give you a new bridge password — "+
				"every calendar and contacts app you have set up will stop working until you enter the new one.")

		dialog.AddResponse("cancel", "Cancel")
		dialog.AddResponse("forget", "Sign in again")
		dialog.SetResponseAppearance("forget", adw.ResponseDestructive)
		dialog.SetDefaultResponse("cancel")
		dialog.SetCloseResponse("cancel")

		dialog.ConnectResponse(func(response string) {
			if response == "forget" {
				// The stored password opens a session that is about to be
				// replaced, so leaving it behind would only confuse the next
				// unlock.
				_ = session.Forget(u.bridge.sessionPath())

				u.pages.Push(u.loginPage())
			}
		})

		dialog.Present(u.window)
	})

	return u.page("Carbonate", scrolled(prefs))
}

// servePage starts and stops the server and shows what to type into a client.
func (u *ui) servePage(bridgePassword string, andStart bool) *adw.NavigationPage {
	prefs := adw.NewPreferencesPage()

	running := adw.NewPreferencesGroup()
	running.SetTitle("Bridge")

	address := adw.NewEntryRow()
	address.SetTitle("Listen on")
	address.SetText(u.prefs.listenOn())
	running.Add(address)

	toggle := primary("Start")

	buttons := adw.NewPreferencesGroup()
	buttons.Add(toggle)

	// Two passwords is the thing newcomers get wrong, so the group says which
	// one this is before showing it. Proton's password signs in to Proton, is
	// used once, and is never stored; the bridge password is one carbonate
	// invented, and is the only one a calendar app ever sees.
	details := adw.NewPreferencesGroup()
	details.SetTitle("Set up your calendar app")
	details.SetDescription(
		"Your apps sign in to Carbonate, not to Proton. Give them the address below, your Proton " +
			"address as the username, and the bridge password — which Carbonate made up. It is not " +
			"your Proton password, and your Proton password will not work here.")

	u.bridgePassword = copyableRow(u, "Bridge password", bridgePassword)
	u.calendarRow = copyableRow(u, "Calendar address", "")
	u.contactsRow = copyableRow(u, "Contacts address", "")

	details.Add(copyableRow(u, "Username", u.bridge.account()))
	details.Add(u.bridgePassword)
	details.Add(u.calendarRow)
	details.Add(u.contactsRow)

	// How often carbonate looks at Proton by itself. See watchChoices.
	checking := adw.NewPreferencesGroup()
	checking.SetTitle("Checking Proton")
	checking.SetDescription(
		"Your calendar app decides how often it looks for new events — GNOME Calendar checks every 30 minutes " +
			"by default, and Carbonate cannot hurry it. This is how often Carbonate looks, so that a change " +
			"coming from Proton shows up in Activity straight away.")

	labels := make([]string, len(watchChoices))
	for i, c := range watchChoices {
		labels[i] = c.label
	}

	u.watch = adw.NewComboRow()
	u.watch.SetTitle("Check Proton")
	u.watch.SetModel(gtk.NewStringList(labels))
	u.watch.SetSelected(watchDefault())
	checking.Add(u.watch)

	activityRow := adw.NewActionRow()
	activityRow.SetTitle("Activity")
	activityRow.SetSubtitle("What the bridge and Proton are doing")
	activityRow.SetActivatable(true)
	activityRow.AddSuffix(gtk.NewImageFromIconName("go-next-symbolic"))
	activityRow.ConnectActivated(func() { u.pages.Push(u.activityPage()) })
	checking.Add(activityRow)

	prefs.Add(running)
	prefs.Add(buttons)
	prefs.Add(details)
	prefs.Add(checking)

	start := func() {
		if u.bridge.running() {
			u.bridge.stop()
			toggle.SetLabel("Start")
			toggle.AddCSSClass("suggested-action")
			address.SetSensitive(true)
			u.watch.SetSensitive(true)
			u.say("Stopped.")

			return
		}

		addr := address.Text()
		every := watchChoices[u.watch.Selected()].every

		if addr != u.prefs.Address {
			u.prefs.Address = addr
			_ = u.prefs.save()
		}

		toggle.SetSensitive(false)

		go func() {
			defer glib.IdleAdd(func() { toggle.SetSensitive(true) })

			if err := u.bridge.start(addr, u.activity, every); err != nil {
				u.say("Could not start: %v", err)

				return
			}

			glib.IdleAdd(func() {
				toggle.SetLabel("Stop")
				toggle.RemoveCSSClass("suggested-action")
				address.SetSensitive(false)
				u.watch.SetSensitive(false)
				u.calendarRow.SetSubtitle("http://" + addr + "/caldav/")
				u.contactsRow.SetSubtitle("http://" + addr + "/carddav/")
			})

			u.say("Serving as %s.", u.bridge.account())
		}()
	}

	toggle.ConnectClicked(start)

	// Started at login, or unlocked from the keyring: serve without being
	// asked, which is the only reason either of those is worth having.
	if andStart {
		start()
	}

	return u.page("Carbonate", scrolled(prefs))
}

// copyableRow shows a value with a button that copies it, since every one of
// these is something the user has to get into another application.
func copyableRow(u *ui, title, value string) *adw.ActionRow {
	row := adw.NewActionRow()
	row.SetTitle(title)
	row.SetSubtitle(value)
	row.AddCSSClass("property")
	row.SetSubtitleSelectable(true)

	button := gtk.NewButtonFromIconName("edit-copy-symbolic")
	button.SetVAlign(gtk.AlignCenter)
	button.AddCSSClass("flat")
	button.SetTooltipText("Copy")

	button.ConnectClicked(func() {
		u.window.Clipboard().SetText(row.Subtitle())
		u.say("%s copied.", title)
	})

	row.AddSuffix(button)

	return row
}

// activityPage is the log, on a page of its own.
//
// `carbonate serve -log` prints this to a terminal. Someone running the window
// has none, and "my calendar will not sync" is exactly the report that cannot
// be answered without it.
func (u *ui) activityPage() *adw.NavigationPage {
	if u.activityNav != nil {
		return u.activityNav
	}

	header := adw.NewHeaderBar()

	copyButton := gtk.NewButtonFromIconName("edit-copy-symbolic")
	copyButton.SetTooltipText("Copy the log, to paste into a bug report")
	copyButton.ConnectClicked(func() {
		u.window.Clipboard().SetText(u.activity.text())
		u.say("Log copied.")
	})

	header.PackEnd(copyButton)

	view := adw.NewToolbarView()
	view.AddTopBar(header)
	view.SetContent(u.activity.widget())

	u.activityNav = adw.NewNavigationPage(view, "Activity")

	return u.activityNav
}
