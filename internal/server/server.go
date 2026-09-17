package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/develonrails/carbonate/internal/caldav"
	"github.com/develonrails/carbonate/internal/carddav"
	"github.com/develonrails/carbonate/internal/proton"
)

// Options are the choices Serve leaves to its caller.
//
// A struct rather than eight parameters: the command line and the GUI both
// call this, and a positional list that long is one where the two quietly
// disagree about which string was the password.
type Options struct {
	// Addr is the address to listen on. Loopback only.
	Addr string

	// Username and Password are what a DAV client must present.
	Username string
	Password string

	// Out receives the banner naming the addresses to connect to.
	Out io.Writer

	// Activity receives a line for every DAV request and for every change
	// Proton reports. It is what `carbonate serve -log` turns on; nil leaves
	// the bridge silent, and a request that failed visible only to the client
	// that received the failure.
	Activity io.Writer

	// Watch is how often to ask Proton whether anything changed, without
	// waiting for a client to ask first. Zero leaves it to the clients.
	//
	// This cannot make a client notice a change any sooner — CalDAV has no
	// way to tell one to come early. It exists so that a change arriving from
	// Proton is visible in Activity even when nothing is connected, and so
	// that the cache is warm when something finally is.
	Watch time.Duration
}

// Serve runs the CalDAV and CardDAV server until the context is cancelled.
//
// It is shared by the command line and the GUI so that both expose exactly
// the same server rather than two that drift apart.
func Serve(ctx context.Context, conn *proton.Conn, opts Options) error {
	addr, username, password := opts.Addr, opts.Username, opts.Password
	out := opts.Out

	// Every line the bridge logs goes through here, so every line is stamped.
	activity := Stamped(opts.Activity)

	calendars := caldav.New(caldav.Logging(caldav.NewStore(conn), activity))
	addressBook := carddav.New(carddav.NewStore(conn, activity))

	go calendars.Watch(ctx, opts.Watch, activity)

	mux := http.NewServeMux()
	mux.Handle("/caldav/", calendars.Handler())
	mux.Handle("/carddav/", addressBook.Handler())

	// Clients given only a hostname find their way from here.
	mux.Handle("/.well-known/caldav", calendars.Handler())
	mux.Handle("/.well-known/carddav", addressBook.Handler())
	mux.HandleFunc("/", Index)

	guarded := Authenticated(username, password, Logged(mux, activity))

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	server := &http.Server{
		Handler:           guarded,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Fprintf(out, `
Serving on http://%[1]s — sign in as %[2]s with your bridge password.

  Calendar   http://%[1]s/caldav/
  Contacts   http://%[1]s/carddav/

GNOME Calendar: Calendars -> Add calendar -> Add from web.
GNOME Contacts has no such dialog; see docs/CLIENTS.md for the one-file
address book setup it reads instead.
`, listener.Addr(), username)

	announce(activity, listener.Addr().String(), opts.Watch)

	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		fmt.Fprintln(out, "\nShutting down.")

		return server.Shutdown(shutdown)

	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return err
	}
}

// announce opens the log by saying what will and will not appear in it.
//
// An empty log is ambiguous in exactly the wrong way: carbonate only talks to
// Proton when a client asks it to, so a bridge nobody has connected to yet
// looks identical to one that cannot see a thing. Saying so costs two lines
// and answers the question before it is asked.
func announce(activity io.Writer, addr string, watch time.Duration) {
	if activity == nil {
		return
	}

	fmt.Fprintf(activity, "carbonate: serving on %s\n", addr)

	if watch > 0 {
		fmt.Fprintf(activity, "carbonate: checking Proton every %s, and whenever an app asks\n", watch)

		return
	}

	fmt.Fprintln(activity, "carbonate: nothing here until an app connects — carbonate asks Proton only when a client does")
}

// authenticated guards the handler with HTTP basic auth.
//
// The listener is on loopback and unencrypted, so this is not protecting the
// wire — it stops other local processes, and anything a browser can be tricked
// into sending, from reaching your calendar.
func Authenticated(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()

		if !ok || !Equal(user, username) || !Equal(pass, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="carbonate", charset="UTF-8"`)
			http.Error(w, "unauthorised", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// equal compares in constant time, so a wrong password cannot be found one
// character at a time.
func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// davCompliance is what the root claims to be. The collections below it
// answer with their own, narrower classes; this is the union, because both
// protocols are served from this one address.
const davCompliance = "1, 3, calendar-access, addressbook"

// index points a browser, or a curious client, at the two collections.
//
// OPTIONS is answered with the compliance classes, because that is how a
// client given only the bare address decides whether this is a WebDAV server
// at all. A plain page with no Dav header reads as "not a WebDAV server", and
// the client stops there — it never reaches the well-known paths, however
// correct those are. GNOME Online Accounts asks exactly this question first,
// and until it was answered, no amount of correctness further down mattered.
func Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	if r.Method == http.MethodOptions {
		w.Header().Set("Dav", davCompliance)
		w.Header().Set("Allow", "OPTIONS, GET, HEAD")
		w.WriteHeader(http.StatusNoContent)

		return
	}

	w.Header().Set("Dav", davCompliance)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "carbonate\n\nCalDAV:  /caldav/\nCardDAV: /carddav/\n")
}
