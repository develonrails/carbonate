package carddav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

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

	// out is where a contact that cannot be read is reported, and reported
	// is how it stays: an address book that is quietly one contact short is
	// worse than one that says which and why.
	out io.Writer

	// said remembers which ids have been reported, because the listing runs
	// on every poll and the same unreadable contact would otherwise fill the
	// log.
	mu   sync.Mutex
	said map[string]bool
}

// NewStore returns a Store reading and writing a Proton account. Contacts
// that cannot be decrypted are reported to out; a nil writer says nothing.
func NewStore(conn *proton.Conn, out io.Writer) Store {
	return &protonStore{conn: conn, out: out, said: make(map[string]bool)}
}

func (s *protonStore) Contacts(ctx context.Context) ([]contacts.Contact, error) {
	people, unreadable, err := contacts.List(ctx, s.conn)
	if err != nil {
		return nil, err
	}

	s.report(unreadable)

	return people, nil
}

// report names each unreadable contact once.
func (s *protonStore) report(ids []string) {
	if s.out == nil || len(ids) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		if s.said[id] {
			continue
		}

		s.said[id] = true

		fmt.Fprintf(s.out, "contact %s: cannot be decrypted, so it is not being served\n", short(id))
	}
}

// short renders a Proton id at a length that identifies it in a log without
// filling the line.
func short(id string) string {
	if len(id) <= 8 {
		return id
	}

	return id[:8] + "…"
}

func (s *protonStore) Put(ctx context.Context, card vcard.Card) (string, bool, error) {
	return contacts.Put(ctx, s.conn, card)
}

func (s *protonStore) Delete(ctx context.Context, uid string) (bool, error) {
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
func (s *protonStore) ChangeToken(ctx context.Context) (string, error) {
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
