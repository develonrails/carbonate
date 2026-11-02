package server

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"

	"github.com/develonrails/carbonate/internal/caldav"
	"github.com/develonrails/carbonate/internal/calendar"
	"github.com/develonrails/carbonate/internal/carddav"
	"github.com/develonrails/carbonate/internal/contacts"
)

// oneCalendar is an account with a single empty calendar: enough to be found,
// which is all discovery is about.
type oneCalendar struct{}

func (oneCalendar) Calendars(context.Context) ([]calendar.Calendar, error) {
	return []calendar.Calendar{{ID: "cal-1", Name: "Personal", Color: "#3584e4"}}, nil
}

func (oneCalendar) Events(context.Context, string) ([]calendar.Event, error) { return nil, nil }

func (oneCalendar) Put(context.Context, string, string) (string, bool, error) {
	return "", false, errors.New("read only")
}

func (oneCalendar) Delete(context.Context, string, string) (bool, error) { return false, nil }

func (oneCalendar) CreateCalendar(context.Context, string, string) (string, error) {
	return "", errors.New("read only")
}

func (oneCalendar) UpdateCalendar(context.Context, string, *string, *string) error {
	return errors.New("read only")
}

func (oneCalendar) ChangeToken(context.Context, string) (string, error) { return "1", nil }
func (oneCalendar) Cursor(context.Context, string) (string, error)      { return "1", nil }

func (oneCalendar) Changes(context.Context, string, string) ([]string, string, bool, error) {
	return nil, "1", false, nil
}

// noContacts is an address book with nothing in it, which still has to be
// found.
type noContacts struct{}

func (noContacts) Contacts(context.Context) ([]contacts.Contact, error) { return nil, nil }

func (noContacts) Put(context.Context, vcard.Card) (string, bool, error) {
	return "", false, errors.New("read only")
}

func (noContacts) Delete(context.Context, string) (bool, error) { return false, nil }
func (noContacts) ChangeToken(context.Context) (string, error)  { return "1", nil }

const (
	testUser     = "someone@proton.me"
	testPassword = "bridge-password"
)

// bridge is everything Serve puts behind its listener, over fake stores.
func bridge(log io.Writer) http.Handler {
	calendars := caldav.New(oneCalendar{}, io.Discard)
	addressBook := carddav.New(noContacts{})

	return Authenticated(testUser, testPassword, Logged(Routes(calendars.Handler(), addressBook.Handler()), log))
}

// TestServeForAClient keeps a bridge over fake stores listening, so that a
// real client can be pointed at it by hand:
//
//	CARBONATE_SERVE_FOR=127.0.0.1:18080 go test ./internal/server -run TestServeForAClient -v
func TestServeForAClient(t *testing.T) {
	addr := os.Getenv("CARBONATE_SERVE_FOR")
	if addr == "" {
		t.Skip("set CARBONATE_SERVE_FOR to an address to listen on")
	}

	server := &http.Server{Addr: addr, Handler: bridge(os.Stderr)}
	go server.ListenAndServe()

	time.Sleep(90 * time.Second)
	server.Close()
}

// discoveryProps is what evolution-data-server asks every address it tries,
// whatever it hopes to find there.
const discoveryProps = `<D:resourcetype/><D:current-user-principal/><D:principal-URL/>` +
	`<C:calendar-home-set/><C:calendar-user-address-set/><A:addressbook-home-set/>`

// propfind asks the way a discovering client does. Redirects are followed
// with the method intact, as libsoup does for PROPFIND.
func propfind(t *testing.T, url, depth, props string) (int, string) {
	t.Helper()

	body := `<?xml version="1.0" encoding="utf-8"?>` +
		`<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:A="urn:ietf:params:xml:ns:carddav">` +
		`<D:prop>` + props + `</D:prop></D:propfind>`

	req, err := http.NewRequest("PROPFIND", url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	req.SetBasicAuth(testUser, testPassword)
	req.Header.Set("Depth", depth)
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, string(got)
}

// multistatus is as much of a PROPFIND answer as discovery reads.
type multistatus struct {
	Responses []struct {
		Href  string `xml:"href"`
		Props []struct {
			Inner string `xml:",innerxml"`
		} `xml:"propstat>prop"`
	} `xml:"response"`
}

func parse(t *testing.T, body string) multistatus {
	t.Helper()

	var ms multistatus
	if err := xml.Unmarshal([]byte(body), &ms); err != nil {
		t.Fatalf("not a multistatus: %v\n%s", err, body)
	}

	return ms
}

// hrefIn returns the address a property points at, or nothing.
func hrefIn(t *testing.T, body, property string) string {
	t.Helper()

	pointer := regexp.MustCompile(`(?s)<(?:\w+:)?` + property + `\b[^>]*>\s*<(?:\w+:)?href\b[^>]*>([^<]+)<`)

	for _, r := range parse(t, body).Responses {
		for _, p := range r.Props {
			if m := pointer.FindStringSubmatch(p.Inner); m != nil {
				return strings.TrimSpace(m[1])
			}
		}
	}

	return ""
}

// collectionsIn lists the members of a home set that are of the given kind.
func collectionsIn(t *testing.T, body, home, kind string) []string {
	t.Helper()

	isKind := regexp.MustCompile(`(?s)<(?:\w+:)?resourcetype\b[^>]*>.*<(?:\w+:)?` + kind + `\b`)

	var found []string

	for _, r := range parse(t, body).Responses {
		if strings.TrimSuffix(r.Href, "/") == strings.TrimSuffix(home, "/") {
			continue
		}

		for _, p := range r.Props {
			if isKind.MatchString(p.Inner) {
				found = append(found, r.Href)
			}
		}
	}

	return found
}

// GNOME Online Accounts hands evolution-data-server the server address alone
// unless the Calendar and Contacts fields are filled in, and the docs say
// they need not be. This is the route it takes from there, measured against
// evolution-data-server 3.60: the root first, then the well-known address for
// each protocol, the principal it leads to, and the home set that names the
// collections.
func TestServerAddressAloneFindsBoth(t *testing.T) {
	server := httptest.NewServer(bridge(io.Discard))
	defer server.Close()

	for _, c := range []struct {
		name, wellKnown, homeSet, kind string
	}{
		{"calendars", "/.well-known/caldav", "calendar-home-set", "calendar"},
		{"address book", "/.well-known/carddav", "addressbook-home-set", "addressbook"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// The root has no principal to give. It has to answer anyway: a
			// 200 is what let the search carry on, where a failure there
			// can end it before the well-known address is ever tried.
			if status, body := propfind(t, server.URL+"/", "0", discoveryProps); status/100 != 2 {
				t.Fatalf("the root answered %d, which can end the search there:\n%s", status, body)
			}

			status, body := propfind(t, server.URL+c.wellKnown, "0", discoveryProps)

			principal := hrefIn(t, body, "current-user-principal")
			if principal == "" {
				t.Fatalf("%s led to no principal (%d):\n%s", c.wellKnown, status, body)
			}

			_, body = propfind(t, server.URL+principal, "0", discoveryProps)

			home := hrefIn(t, body, c.homeSet)
			if home == "" {
				t.Fatalf("the principal %s names no %s:\n%s", principal, c.homeSet, body)
			}

			_, body = propfind(t, server.URL+home, "1", `<D:resourcetype/><D:displayname/>`)

			if found := collectionsIn(t, body, home, c.kind); len(found) == 0 {
				t.Errorf("the home set %s lists no %s:\n%s", home, c.kind, body)
			}
		})
	}
}
