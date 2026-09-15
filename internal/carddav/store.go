package carddav

import (
	"context"

	"github.com/emersion/go-vcard"

	"github.com/develonrails/carbonate/internal/contacts"
	"github.com/develonrails/carbonate/internal/proton"
)

// Store is everything the CardDAV backend needs from Proton, so that the
// backend can be exercised without an account.
type Store interface {
	// Contacts returns every contact, decrypted.
	Contacts(ctx context.Context) ([]contacts.Contact, error)

	// Put creates a contact or replaces the one with the same UID.
	Put(ctx context.Context, card vcard.Card) (id string, created bool, err error)

	// Delete removes the contact with the given UID, reporting whether one
	// was there.
	Delete(ctx context.Context, uid string) (bool, error)
}

type protonStore struct {
	conn *proton.Conn
}

// NewStore returns a Store reading and writing a Proton account.
func NewStore(conn *proton.Conn) Store {
	return protonStore{conn: conn}
}

func (s protonStore) Contacts(ctx context.Context) ([]contacts.Contact, error) {
	return contacts.List(ctx, s.conn)
}

func (s protonStore) Put(ctx context.Context, card vcard.Card) (string, bool, error) {
	return contacts.Put(ctx, s.conn, card)
}

func (s protonStore) Delete(ctx context.Context, uid string) (bool, error) {
	return contacts.Delete(ctx, s.conn, uid)
}
