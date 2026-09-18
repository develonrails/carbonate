// Package protontest runs a fake Proton for carbonate's tests.
//
// go-proton-api ships one, and it covers authentication well: login, key
// unlocking, token rotation and session revocation all run against it without
// an account or a network. What it does not cover, carbonate adds here.
//
// Owning that layer is the point. Every time the real Proton turns out to do
// something other than what we assumed — and it has, repeatedly — the fake has
// to learn the same thing, or the next test run will happily agree with the
// old assumption. A fake nobody updates is worse than no fake: it reports
// confidence it has not earned.
//
// What this layer knows that the upstream one does not:
//
//   - The bulk export endpoint, /contacts/v4/contacts/export. carbonate reads
//     an address book with it because the alternative is one request per
//     contact.
//   - Deleting contacts, PUT /contacts/v4/delete, which upstream does not
//     implement at all.
//   - An address book with nothing in it. Upstream pages the listing with
//     Chunk(values, pageSize)[page], which panics on an empty account rather
//     than returning an empty page — and an empty account is where every test
//     starts, and where every new Proton user does too.
package protontest

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"testing"

	"github.com/ProtonMail/go-proton-api/server"
)

// Server is the fake Proton: go-proton-api's, with carbonate's additions in
// front of it.
type Server struct {
	upstream *server.Server
	front    *httptest.Server

	// contacts is served here rather than upstream, whose model of a contact
	// disagrees with Proton's.
	contacts *contacts
}

// New starts a fake Proton and points carbonate at it for the test's
// duration.
func New(t *testing.T) *Server {
	t.Helper()

	// Plain HTTP: the fake server's TLS certificate is self-signed, and
	// go-proton-api verifies certificates.
	upstream := server.New(server.WithTLS(false))
	t.Cleanup(upstream.Close)

	target, err := url.Parse(upstream.GetHostURL())
	if err != nil {
		t.Fatalf("parsing the fake server URL: %v", err)
	}

	s := &Server{upstream: upstream, contacts: newContacts()}

	mux := http.NewServeMux()
	mux.HandleFunc("/contacts/v4", s.serveContacts)
	mux.HandleFunc("/contacts/v4/", s.serveContacts)
	mux.Handle("/", httputil.NewSingleHostReverseProxy(target))

	s.front = httptest.NewServer(mux)
	t.Cleanup(s.front.Close)

	t.Setenv("CARBONATE_API_URL", s.front.URL)

	return s
}

// CreateUser adds an account, returning its ID.
func (s *Server) CreateUser(t *testing.T, username, password string) string {
	t.Helper()

	id, _, err := s.upstream.CreateUser(username, []byte(password))
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	return id
}

// URL is where carbonate should send its requests.
func (s *Server) URL() string {
	return s.front.URL
}

func replaceBody(resp *http.Response, body []byte) error {
	resp.Body = io.NopCloser(newReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))

	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// newReader is bytes.NewReader, named here so the import list stays short in
// a file that is mostly HTTP plumbing.
func newReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

// errUnexpectedPrompt ends a login that asked for something a test account
// should never be asked for.
var errUnexpectedPrompt = errors.New("the fake Proton asked for input a test cannot give")
