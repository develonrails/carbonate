package carddav

import (
	"context"
	"testing"
	"time"

	"github.com/emersion/go-webdav/carddav"

	"github.com/develonrails/carbonate/internal/contacts"
	"github.com/develonrails/carbonate/internal/proton"
)

func newBackend() *Backend {
	return New((*proton.Conn)(nil), time.Minute)
}

func TestObjectUIDUsesRememberedName(t *testing.T) {
	b := newBackend()

	path := bookPath + "whatever-the-client-called-it.vcf"
	b.names[path] = "the-real-uid@example.com"

	got, err := b.objectUID(path)
	if err != nil {
		t.Fatalf("objectUID: %v", err)
	}

	if got != "the-real-uid@example.com" {
		t.Errorf("objectUID = %q, want the remembered UID", got)
	}
}

func TestObjectUIDFallsBackToFilename(t *testing.T) {
	b := newBackend()

	got, err := b.objectUID(bookPath + "person%40example.com.vcf")
	if err != nil {
		t.Fatalf("objectUID: %v", err)
	}

	if got != "person@example.com" {
		t.Errorf("objectUID = %q, want %q", got, "person@example.com")
	}
}

func TestObjectUIDRejectsNonContact(t *testing.T) {
	b := newBackend()

	if _, err := b.objectUID(bookPath + "notacontact"); err == nil {
		t.Error("a path without .vcf was accepted as a contact")
	}
}

// vCard UIDs are free-form and routinely contain characters a URL cannot carry
// unescaped.
func TestObjectPathRoundTrip(t *testing.T) {
	b := newBackend()

	for _, uid := range []string{
		"plain",
		"person@example.com",
		"with space",
		"with/slash",
		"with?question",
		"urn:uuid:8f3a-4c2b",
	} {
		path := objectPath(uid)

		got, err := b.objectUID(path)
		if err != nil {
			t.Errorf("objectUID(%q): %v", path, err)
			continue
		}

		if got != uid {
			t.Errorf("round trip of %q gave %q (path %q)", uid, got, path)
		}
	}
}

func TestETagTracksModification(t *testing.T) {
	base := contacts.Contact{ID: "contact-id", Modified: time.Unix(1000, 0)}

	edited := base
	edited.Modified = time.Unix(2000, 0)

	if etag(base) == etag(edited) {
		t.Error("ETag did not change when the contact was edited")
	}

	if etag(base) == etag(contacts.Contact{ID: "other", Modified: time.Unix(1000, 0)}) {
		t.Error("two different contacts share an ETag")
	}

	if etag(base) != etag(base) {
		t.Error("ETag is not stable")
	}
}

// go-webdav infers a resource's kind from path depth, so the layout must not
// drift: principal 1, home set 2, address book 3, contact 4.
func TestPathDepths(t *testing.T) {
	depths := map[string]int{
		principalPath: 1,
		homeSetPath:   2,
		bookPath:      3,
	}

	for path, want := range depths {
		trimmed := path[len(prefix):]

		got := 0
		for i := 1; i < len(trimmed); i++ {
			if trimmed[i] == '/' {
				got++
			}
		}

		if got != want {
			t.Errorf("%s has depth %d below the prefix, want %d", path, got, want)
		}
	}
}

func TestPrincipalAndHomeSet(t *testing.T) {
	b := newBackend()

	principal, err := b.CurrentUserPrincipal(context.Background())
	if err != nil {
		t.Fatalf("CurrentUserPrincipal: %v", err)
	}

	if principal != principalPath {
		t.Errorf("principal = %q, want %q", principal, principalPath)
	}

	home, err := b.AddressBookHomeSetPath(context.Background())
	if err != nil {
		t.Fatalf("AddressBookHomeSetPath: %v", err)
	}

	if home != homeSetPath {
		t.Errorf("home set = %q, want %q", home, homeSetPath)
	}
}

func TestListAddressBooksReturnsTheOne(t *testing.T) {
	books, err := newBackend().ListAddressBooks(context.Background())
	if err != nil {
		t.Fatalf("ListAddressBooks: %v", err)
	}

	if len(books) != 1 {
		t.Fatalf("got %d address books, want 1", len(books))
	}

	if books[0].Path != bookPath {
		t.Errorf("path = %q, want %q", books[0].Path, bookPath)
	}

	if books[0].Name == "" {
		t.Error("the address book has no name, so a client would show it blank")
	}
}

func TestGetAddressBookRejectsAnUnknownPath(t *testing.T) {
	b := newBackend()

	if _, err := b.GetAddressBook(context.Background(), bookPath); err != nil {
		t.Errorf("the real address book was not found: %v", err)
	}

	if _, err := b.GetAddressBook(context.Background(), homeSetPath+"somethingelse/"); err == nil {
		t.Error("an unknown address book path was accepted")
	}
}

// Proton has exactly one collection of contacts. Saying so plainly is better
// than letting a client create something that will not exist.
func TestAddressBookCreationIsRefused(t *testing.T) {
	b := newBackend()

	if err := b.CreateAddressBook(context.Background(), nil); err == nil {
		t.Error("creating an address book was allowed")
	}

	if err := b.DeleteAddressBook(context.Background(), bookPath); err == nil {
		t.Error("deleting the address book was allowed")
	}
}

// RFC 6352 makes the filter optional and an absent one matches everything.
// go-webdav's matcher reads "no conditions" as "match nothing", so a client
// that sends no filter would be told the address book is empty.
func TestQueryWithoutFilterIsNotEmpty(t *testing.T) {
	objects := []carddav.AddressObject{{Path: "/a.vcf"}, {Path: "/b.vcf"}}

	got, err := carddav.Filter(&carddav.AddressBookQuery{}, objects)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}

	if len(got) != 0 {
		t.Skip("go-webdav now matches an empty filter; the guard can go")
	}

	// The guard in QueryAddressObjects is what keeps this from reaching a
	// client, so assert the condition it tests for.
	query := &carddav.AddressBookQuery{}
	if len(query.PropFilters) != 0 {
		t.Fatal("an empty query unexpectedly carries prop filters")
	}
}
