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
	"github.com/develonrails/carbonate/internal/proton"
)

// cacheTTL is how long a fetched calendar is reused. Long enough that a
// client polling every few seconds does not hammer Proton, short enough that
// a change made in the Proton web app shows up while you wait.
const cacheTTL = 30 * time.Second

// serveDAV runs the CalDAV server until the context is cancelled.
func serveDAV(ctx context.Context, conn *proton.Conn, addr, username, password string, out io.Writer) error {
	backend := caldav.New(conn, cacheTTL)

	mux := http.NewServeMux()
	mux.Handle("/", authenticated(username, password, backend.Handler()))

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	fmt.Fprintf(out, "Serving CalDAV on http://%s\n", listener.Addr())
	fmt.Fprintf(out, "In GNOME Calendar: Add calendar -> Add from web, URL http://%s/, user %s, password is your bridge password.\n",
		listener.Addr(), username)

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
