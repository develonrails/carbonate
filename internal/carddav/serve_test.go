package carddav

import (
	"context"
	"errors"
	"testing"

	"github.com/emersion/go-vcard"
)

func TestListAddressObjects(t *testing.T) {
	b := New(newFakeContacts())

	objects, err := b.ListAddressObjects(context.Background(), bookPath, nil)
	if err != nil {
		t.Fatalf("ListAddressObjects: %v", err)
	}

	if len(objects) != 1 {
		t.Fatalf("got %d objects, want 1", len(objects))
	}

	if objects[0].ETag == "" {
		t.Error("the contact has no ETag, so a client can never skip it")
	}

	if objects[0].Card.Value(vcard.FieldFormattedName) != "Jan Jansen" {
		t.Errorf("card = %v", objects[0].Card)
	}
}

func TestGetAddressObjectFindsByUID(t *testing.T) {
	b := New(newFakeContacts())

	obj, err := b.GetAddressObject(context.Background(), objectPath("person@example.com"), nil)
	if err != nil {
		t.Fatalf("GetAddressObject: %v", err)
	}

	if obj.Card.Value(vcard.FieldUID) != "person@example.com" {
		t.Errorf("got the wrong contact: %v", obj.Card)
	}
}

func TestGetAddressObjectRejectsAnUnknownUID(t *testing.T) {
	b := New(newFakeContacts())

	if _, err := b.GetAddressObject(context.Background(), objectPath("nobody@example.com"), nil); err == nil {
		t.Error("an unknown contact was found")
	}
}

// A client may name a resource whatever it likes, and the answer has to come
// back at the name it asked for.
func TestPutAnswersAtTheClientsPath(t *testing.T) {
	fake := newFakeContacts()
	b := New(fake)

	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "new@example.com")
	card.SetValue(vcard.FieldFormattedName, "New Person")

	path := bookPath + "whatever-i-called-it.vcf"

	obj, err := b.PutAddressObject(context.Background(), path, card, nil)
	if err != nil {
		t.Fatalf("PutAddressObject: %v", err)
	}

	if obj.Path != path {
		t.Errorf("answered at %q, want the path the client used", obj.Path)
	}

	if len(fake.put) != 1 {
		t.Errorf("stored %d contacts, want 1", len(fake.put))
	}
}

func TestPutRejectsACardWithoutUID(t *testing.T) {
	b := New(newFakeContacts())

	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")

	if _, err := b.PutAddressObject(context.Background(), bookPath+"x.vcf", card, nil); err == nil {
		t.Error("a card with no UID was accepted, leaving nothing to address it by")
	}
}

func TestDeleteRemovesTheContact(t *testing.T) {
	fake := newFakeContacts()
	b := New(fake)

	if err := b.DeleteAddressObject(context.Background(), objectPath("person@example.com")); err != nil {
		t.Fatalf("DeleteAddressObject: %v", err)
	}

	if len(fake.deleted) != 1 || fake.deleted[0] != "person@example.com" {
		t.Errorf("deleted %v, want the contact's UID", fake.deleted)
	}
}

func TestDeleteReportsAMissingContact(t *testing.T) {
	b := New(newFakeContacts())

	if err := b.DeleteAddressObject(context.Background(), objectPath("nobody@example.com")); err == nil {
		t.Error("deleting a contact that is not there succeeded")
	}
}

// RFC 6352 makes the filter optional and an absent one matches everything.
func TestQueryWithoutFilterReturnsEverything(t *testing.T) {
	b := New(newFakeContacts())

	got, err := b.QueryAddressObjects(context.Background(), bookPath, nil)
	if err != nil {
		t.Fatalf("QueryAddressObjects: %v", err)
	}

	if len(got) != 1 {
		t.Errorf("got %d contacts, want 1 — an unfiltered query must not come back empty", len(got))
	}
}

// A failure must surface rather than be served as an empty address book,
// which a client would read as every contact having been deleted.
func TestStoreFailureIsReported(t *testing.T) {
	fake := newFakeContacts()
	fake.err = errors.New("Proton is unreachable")

	if _, err := New(fake).ListAddressObjects(context.Background(), bookPath, nil); err == nil {
		t.Error("a failing store produced an empty address book instead of an error")
	}
}
