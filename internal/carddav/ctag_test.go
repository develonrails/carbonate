package carddav

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-vcard"

	"github.com/develonrails/carbonate/internal/contacts"
)

const ctagRequest = `<?xml version="1.0" encoding="UTF-8"?>
<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><cs:getctag/></d:prop></d:propfind>`

// propfind runs a PROPFIND against the address book and returns the body.
func propfind(t *testing.T, b *Backend, body string) string {
	t.Helper()

	req := httptest.NewRequest("PROPFIND", bookPath, strings.NewReader(body))
	req.Header.Set("Depth", "0")
	req.Header.Set("Content-Type", "application/xml")

	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)

	return rec.Body.String()
}

// Without a ctag a client fetches the address book once and never again: it
// has nothing to compare, so nothing tells it a contact appeared somewhere
// else. Writing from the client keeps working, which makes the gap look like
// a one-way bridge rather than a missing property.
func TestAddressBookAnswersGetctag(t *testing.T) {
	body := propfind(t, New(newFakeContacts()), ctagRequest)

	if !strings.Contains(body, "getctag") {
		t.Fatalf("no getctag in the reply:\n%s", body)
	}

	if strings.Contains(body, "404") {
		t.Errorf("getctag was answered with a 404 propstat:\n%s", body)
	}
}

// The token has to move when a contact does, or it is decoration.
func TestGetctagFollowsAChange(t *testing.T) {
	fake := newFakeContacts()
	b := New(fake)

	before := propfind(t, b, ctagRequest)

	card := make(vcard.Card)
	card.SetValue(vcard.FieldUID, "someone-else@example.com")
	fake.people = append(fake.people, contacts.Contact{
		ID:       "contact-2",
		UID:      "someone-else@example.com",
		Modified: time.Unix(2000, 0),
		Card:     card,
	})

	if after := propfind(t, b, ctagRequest); after == before {
		t.Errorf("the ctag did not move after a contact was added:\n%s", after)
	}
}

// Reading the token must not cost a decryption of every contact, because a
// client asks for it on every poll — that is the whole point of having one.
func TestGetctagDoesNotListContacts(t *testing.T) {
	fake := newFakeContacts()

	propfind(t, New(fake), ctagRequest)

	if fake.listCalls != 0 {
		t.Errorf("answering getctag fetched the contacts %d times", fake.listCalls)
	}

	if fake.tokenCalls == 0 {
		t.Error("the change token was never asked for")
	}
}
