package carddav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

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

	// ChangeToken returns a value that changes whenever any contact does,
	// and stays put when none do.
	ChangeToken(ctx context.Context) (string, error)
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

// ChangeToken summarises the contact listing: every id and when it last
// changed.
//
// The listing is metadata only, so this costs one request and no decryption —
// which is the point, since a client asks for it on every poll. Hashing the
// pairs catches all three kinds of change: a new id appears, an id's time
// moves, an id goes away.
//
// The core event loop would be cheaper still, but its latest id moves when
// any mail arrives, and every one of those would send the client off to
// refetch and decrypt an address book that had not changed.
func (s protonStore) ChangeToken(ctx context.Context) (string, error) {
	index, err := s.conn.Client.GetAllContacts(ctx)
	if err != nil {
		return "", fmt.Errorf("listing contacts: %w", err)
	}

	pairs := make([]string, 0, len(index))
	for _, meta := range index {
		pairs = append(pairs, meta.ID+":"+strconv.FormatInt(meta.ModifyTime, 10))
	}

	// Proton gives no order guarantee, and a token that depends on the order
	// of the reply would change on its own.
	sort.Strings(pairs)

	sum := sha256.Sum256([]byte(strings.Join(pairs, "\n")))

	return hex.EncodeToString(sum[:8]), nil
}
