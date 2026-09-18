package contacts_test

import (
	"context"
	"testing"

	"github.com/emersion/go-vcard"

	"github.com/develonrails/carbonate/internal/contacts"
	"github.com/develonrails/carbonate/internal/protontest"
)

// card builds a vCard with a name, an address and a phone number, so that a
// test covers both halves of the split: the fields Proton must be able to
// read, and the ones it must not.
func card(uid, name string) vcard.Card {
	c := make(vcard.Card)
	c.SetValue(vcard.FieldVersion, "4.0")
	c.SetValue(vcard.FieldUID, uid)
	c.SetValue(vcard.FieldFormattedName, name)
	c.SetValue(vcard.FieldEmail, "someone@example.com")
	c.SetValue(vcard.FieldTelephone, "+31600000000")

	return c
}

func TestPutThenListReturnsTheContact(t *testing.T) {
	conn := protontest.New(t).Connect(t)
	ctx := context.Background()

	if _, created, err := contacts.Put(ctx, conn, card("one@example.com", "Jan Jansen")); err != nil {
		t.Fatalf("Put: %v", err)
	} else if !created {
		t.Error("the first write of a UID reported a replacement")
	}

	all, unreadable, err := contacts.List(ctx, conn)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(unreadable) != 0 {
		t.Errorf("carbonate could not read back what it wrote: %v", unreadable)
	}

	if len(all) != 1 {
		t.Fatalf("got %d contacts, want 1", len(all))
	}

	if got := all[0].Card.Value(vcard.FieldFormattedName); got != "Jan Jansen" {
		t.Errorf("FN = %q", got)
	}

	// The phone number lives in the encrypted card. Getting it back proves
	// the encrypted half survived the round trip, not just the signed one.
	if got := all[0].Card.Value(vcard.FieldTelephone); got != "+31600000000" {
		t.Errorf("TEL = %q, want the number back from the encrypted card", got)
	}
}

// Writing the same UID twice must replace rather than accumulate. A client
// that re-saves a contact would otherwise fill the address book with copies.
func TestPutTwiceReplaces(t *testing.T) {
	conn := protontest.New(t).Connect(t)
	ctx := context.Background()

	if _, _, err := contacts.Put(ctx, conn, card("one@example.com", "Jan Jansen")); err != nil {
		t.Fatalf("first Put: %v", err)
	}

	_, created, err := contacts.Put(ctx, conn, card("one@example.com", "Jan de Jong"))
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	if created {
		t.Error("writing an existing UID reported a creation")
	}

	all, _, err := contacts.List(ctx, conn)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(all) != 1 {
		t.Fatalf("got %d contacts, want 1 — the second write did not replace", len(all))
	}

	if got := all[0].Card.Value(vcard.FieldFormattedName); got != "Jan de Jong" {
		t.Errorf("FN = %q, want the second write", got)
	}
}

func TestDeleteRemovesTheContact(t *testing.T) {
	conn := protontest.New(t).Connect(t)
	ctx := context.Background()

	if _, _, err := contacts.Put(ctx, conn, card("one@example.com", "Jan Jansen")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	found, err := contacts.Delete(ctx, conn, "one@example.com")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if !found {
		t.Error("Delete did not find the contact it had just written")
	}

	all, _, err := contacts.List(ctx, conn)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(all) != 0 {
		t.Errorf("the contact is still listed after being deleted: %v", all)
	}
}

func TestDeleteReportsAnUnknownUID(t *testing.T) {
	conn := protontest.New(t).Connect(t)

	found, err := contacts.Delete(context.Background(), conn, "never-written@example.com")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if found {
		t.Error("Delete claimed to have removed a contact that was never there")
	}
}

// Proton encrypts and signs a contact with the account's user key, and reads
// it back with that key alone. carbonate used the address key, and the result
// was a contact Proton itself could not open — invisible from this side,
// because carbonate could read back everything it had written.
//
// So the check is not "can we read our own write", which passed throughout.
// It is "can the user key alone read it", which is what Proton will do.
func TestContactsAreEncryptedToTheUserKey(t *testing.T) {
	conn := protontest.New(t).Connect(t)
	ctx := context.Background()

	if _, _, err := contacts.Put(ctx, conn, card("one@example.com", "Jan Jansen")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	index, err := conn.Client.GetAllContacts(ctx)
	if err != nil {
		t.Fatalf("GetAllContacts: %v", err)
	}

	if len(index) != 1 {
		t.Fatalf("got %d contacts, want 1", len(index))
	}

	stored, err := conn.Client.GetContact(ctx, index[0].ID)
	if err != nil {
		t.Fatalf("GetContact: %v", err)
	}

	if _, err := stored.Cards.Merge(conn.UserKR); err != nil {
		t.Fatalf("Proton could not open the contact carbonate wrote: %v", err)
	}
}
