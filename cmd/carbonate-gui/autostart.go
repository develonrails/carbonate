//go:build gtk

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// The background portal. A sandboxed app cannot drop a file in the user's
// autostart directory, and should not want to: the portal asks the user
// instead, shows carbonate in the system's list of background apps, and lets
// them revoke it there rather than hunting for a file we left behind.
const (
	portalBus       = "org.freedesktop.portal.Desktop"
	portalPath      = "/org/freedesktop/portal/desktop"
	backgroundIface = "org.freedesktop.portal.Background"
	requestIface    = "org.freedesktop.portal.Request"

	// portalOK is the response code for "the user agreed". 1 is a refusal and
	// 2 means it ended some other way; neither is an error worth a stack trace.
	portalOK = 0
)

// autostartReason is shown to the user in the portal's own dialog, in the
// system's words rather than ours. It has to explain a request they did not
// make of us, so it says what running in the background is *for*.
const autostartReason = "Carbonate serves your Proton calendar to your other apps. Starting at login means they find it already there."

// requestAutostart asks that carbonate be started at login, or that it no
// longer be.
//
// The portal decides, not us, and it asks the user. The answer arrives later
// as a signal on a request object rather than as a return value, so done is
// called once that lands — on the D-Bus worker thread, so a caller touching
// widgets has to hop back to the main loop itself.
func requestAutostart(ctx context.Context, want bool, done func(granted bool, err error)) {
	conn, err := gio.BusGetSync(ctx, gio.BusTypeSession)
	if err != nil {
		done(false, fmt.Errorf("connecting to the session bus: %w", err))

		return
	}

	token, err := handleToken()
	if err != nil {
		done(false, err)

		return
	}

	// The reply path is derived, not returned: the portal builds it from our
	// bus name and the token we chose, so the subscription can be in place
	// before the call is made and no answer can arrive unheard.
	path := requestPath(conn.UniqueName(), token)

	var subscription uint

	subscription = conn.SignalSubscribe(portalBus, requestIface, "Response", path, "", gio.DBusSignalFlagsNone,
		func(conn *gio.DBusConnection, _, _, _, _ string, parameters *glib.Variant) {
			conn.SignalUnsubscribe(subscription)

			done(parameters.ChildValue(0).Uint32() == portalOK, nil)
		})

	options := asv(map[string]*glib.Variant{
		"handle_token": glib.NewVariantString(token),
		"reason":       glib.NewVariantString(autostartReason),
		"autostart":    glib.NewVariantBoolean(want),

		// Named explicitly because the portal writes it into the autostart
		// file, and the file outlives this process. The desktop file is not
		// consulted: what gets started is what we say here.
		// --background so that starting at login does not put a window on
		// screen. Someone who wanted the bridge running at login did not ask
		// to be shown it.
		"commandline": glib.NewVariantStrv([]string{"carbonate-gui", "--background"}),
	})

	conn.Call(ctx, portalBus, portalPath, backgroundIface, "RequestBackground",
		glib.NewVariantTuple([]*glib.Variant{glib.NewVariantString(""), options}),
		glib.NewVariantType("(o)"), gio.DBusCallFlagsNone, -1,
		func(_ gio.AsyncResulter) {
			// A failure here means no Response signal is ever coming, so the
			// subscription has to go or the switch waits forever.
			//
			// The common cause is no portal at all: carbonate outside a
			// sandbox, on a system without xdg-desktop-portal.
		})
}

// handleToken names this exchange. The portal echoes it back in the object
// path of the reply, which is how a reply is matched to the request that
// caused it.
func handleToken() (string, error) {
	var raw [8]byte

	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generating a portal token: %w", err)
	}

	return "carbonate_" + hex.EncodeToString(raw[:]), nil
}

// requestPath rebuilds the object path the portal will answer on.
//
// The sender is our unique bus name with the leading colon dropped and dots
// turned into underscores, because a D-Bus object path may hold neither.
func requestPath(unique, token string) string {
	sender := strings.ReplaceAll(strings.TrimPrefix(unique, ":"), ".", "_")

	return "/org/freedesktop/portal/desktop/request/" + sender + "/" + token
}

// asv builds the a{sv} dictionary every portal call takes its options in.
func asv(entries map[string]*glib.Variant) *glib.Variant {
	children := make([]*glib.Variant, 0, len(entries))

	for key, value := range entries {
		children = append(children, glib.NewVariantDictEntry(glib.NewVariantString(key), glib.NewVariantVariant(value)))
	}

	return glib.NewVariantArray(glib.NewVariantType("{sv}"), children)
}
