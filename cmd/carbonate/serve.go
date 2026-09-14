package main

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

// cacheTTL is how long a fetched calendar is reused. Long enough that a
// client polling every few seconds does not hammer Proton, short enough that
// a change made in the Proton web app shows up while you wait.
const cacheTTL = 30 * time.Second

// serveDAV runs the CalDAV server until the context is cancelled.
func serveDAV(ctx context.Context, conn *proton.Conn, addr, username, password string, out io.Writer) error {
	calendars := caldav.New(conn, cacheTTL)
	addressBook := carddav.New(conn, cacheTTL)

	mux := http.NewServeMux()
	mux.Handle("/caldav/", calendars.Handler())
	mux.Handle("/carddav/", addressBook.Handler())

	// Clients given only a hostname find their way from here.
	mux.Handle("/.well-known/caldav", calendars.Handler())
	mux.Handle("/.well-known/carddav", addressBook.Handler())
	mux.HandleFunc("/", index)

	guarded := authenticated(username, password, mux)

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
GNOME Contacts goes through Online Accounts, or add the CardDAV address in
Evolution directly.
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
func authenticated(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()

		if !ok || !equal(user, username) || !equal(pass, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="carbonate", charset="UTF-8"`)
			http.Error(w, "unauthorised", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// equal compares in constant time, so a wrong password cannot be found one
// character at a time.
func equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// index points a browser, or a curious client, at the two collections.
func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprint(w, "carbonate\n\nCalDAV:  /caldav/\nCardDAV: /carddav/\n")
}
