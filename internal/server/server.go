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

// Serve runs the CalDAV and CardDAV server until the context is cancelled.
//
// It is shared by the command line and the GUI so that both expose exactly
// the same server rather than two that drift apart.
func Serve(ctx context.Context, conn *proton.Conn, addr, username, password string, out io.Writer) error {
	calendars := caldav.New(caldav.NewStore(conn))
	addressBook := carddav.New(carddav.NewStore(conn))

	mux := http.NewServeMux()
	mux.Handle("/caldav/", calendars.Handler())
	mux.Handle("/carddav/", addressBook.Handler())

	// Clients given only a hostname find their way from here.
	mux.Handle("/.well-known/caldav", calendars.Handler())
	mux.Handle("/.well-known/carddav", addressBook.Handler())
	mux.HandleFunc("/", Index)

	guarded := Authenticated(username, password, mux)

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

// index points a browser, or a curious client, at the two collections.
func Index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "carbonate\n\nCalDAV:  /caldav/\nCardDAV: /carddav/\n")
}
