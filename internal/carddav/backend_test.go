package carddav

import (
	"testing"
	"time"

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
