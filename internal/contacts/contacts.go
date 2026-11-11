// Package contacts reads and writes Proton contacts as vCards.
//
// Proton stores a contact as a set of cards, one per protection level, and
// each card is itself a vCard. Reading merges them; writing splits the vCard
// back apart. Unlike calendar events, the shape needs no reassembly work of
// our own: go-proton-api merges the cards for us.
package contacts

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/emersion/go-vcard"

	"github.com/develonrails/carbonate/internal/proton"
)

// fieldPMTLS has no constant in go-proton-api, unlike the other X-PM-* fields.
const fieldPMTLS = "X-PM-TLS"

// signedFields stay in the signed, unencrypted card.
//
// Proton needs to read them: it addresses mail by EMAIL, shows FN in its own
// UI, and hangs per-address encryption preferences off the email's group.
// Everything else — phone numbers, addresses, notes, birthdays — is encrypted.
//
// This is SIGNED_FIELDS from Proton's web client
// (packages/shared/lib/contacts/constants.ts), and the list has to match it
// exactly rather than merely overlap. Proton reads a contact's pinned keys
// and per-address encryption settings from the signed card and from nowhere
// else — getPublicKeysVcardHelper.ts says so in as many words, "all the info
// we need is in the signed part", and gives up with no pinned keys if the
// field is not there. A key or an X-PM-* setting moved into the encrypted
// card is therefore not merely relocated: Proton stops honouring it, so a
// pinned key goes unused and forced encryption to an external address
// quietly turns itself off. Nothing reports this, and carbonate would cause
// it on every write of a contact it had read back correctly.
var signedFields = []string{
	vcard.FieldVersion,
	vcard.FieldProductID,
	vcard.FieldFormattedName,
	vcard.FieldUID,
	vcard.FieldEmail,

	vcard.FieldKey,
	api.FieldPMMIMEType,
	api.FieldPMEncrypt,
	api.FieldPMEncryptUntrusted,
	api.FieldPMSign,
	api.FieldPMScheme,
	fieldPMTLS,
}

// clearFields go in a cleartext card, which a contact has only when it names
// categories.
//
// CATEGORIES is how contact group membership is stored, and the server is
// what maintains it — so unlike everything else outside the signed card, it
// cannot be encrypted. Proton's web client builds a cleartext card for
// exactly this case and for no other (CLEAR_FIELDS in contacts/constants.ts),
// and VERSION and PRODID ride along so the card stands on its own.
var clearFields = []string{
	vcard.FieldVersion,
	vcard.FieldProductID,
	vcard.FieldCategories,
}

// How Proton numbers the two flags on a create.
//
// Overwrite settles what happens when the UID is already taken: carbonate
// takes the contact over, because a CardDAV PUT is the client saying this is
// now the contact at that address. The web app refuses instead, which is the
// right answer for an import and the wrong one here.
//
// Labels settles whether the server reads CATEGORIES out of the cleartext
// card and files the contact under those groups. Left at ignore, a contact
// written with categories joins no group at all.
const (
	overwriteContact = 1

	labelsIgnore  = 0
	labelsInclude = 1
)

// labelsFor reports whether Proton should act on the contact's categories.
func labelsFor(card vcard.Card) int {
	if len(card[vcard.FieldCategories]) == 0 {
		return labelsIgnore
	}

	return labelsInclude
}

// Contact is a decrypted Proton contact.
type Contact struct {
	ID       string
	UID      string
	Modified time.Time
	Card     vcard.Card
}

// List returns every contact that can be decrypted, and the ids of those that
// cannot.
//
// A contact that cannot be decrypted is skipped rather than failing the
// listing. The two are not close: a client that gets an error shows no
// address book at all, so a single unreadable card would hide every readable
// one behind it. Skipping costs that one contact and keeps the rest
// reachable.
//
// The skipped ids come back rather than disappearing, because a contact that
// silently goes missing is the kind of fault nobody reports until long after
// it started.
func List(ctx context.Context, conn *proton.Conn) ([]Contact, []string, error) {
	kr, err := conn.ContactKeyRing(ctx)
	if err != nil {
		return nil, nil, err
	}

	// Two requests rather than one per contact: the listing carries the
	// metadata but no cards, and the export carries the cards but not when
	// each was last changed — which the ETag depends on.
	index, err := conn.Client.GetAllContacts(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching contacts: %w", err)
	}

	modified := make(map[string]int64, len(index))
	for _, meta := range index {
		modified[meta.ID] = meta.ModifyTime
	}

	exported, err := exportAll(ctx, conn)
	if err != nil {
		return nil, nil, err
	}

	out := make([]Contact, 0, len(exported))

	var unreadable []string

	for _, r := range exported {
		card, err := r.Cards.Merge(kr)
		if err != nil {
			unreadable = append(unreadable, r.ID)

			continue
		}

		out = append(out, Contact{
			ID:       r.ID,
			UID:      uidOf(card, api.Contact{ContactMetadata: api.ContactMetadata{ID: r.ID}}),
			Modified: time.Unix(modified[r.ID], 0),
			Card:     card,
		})
	}

	return out, unreadable, nil
}

// exportedContact is a contact as the export endpoint returns it: the cards,
// decryptable, without the metadata the listing carries.
type exportedContact struct {
	ID    string
	Cards api.Cards
}

// exportPageSize is what Proton's own web client asks for.
const exportPageSize = 50

// exportAll fetches every contact's cards, a page at a time.
//
// go-proton-api has no call for this, and its per-contact endpoint would mean
// a request each — fine for a handful, ruinous for an address book.
func exportAll(ctx context.Context, conn *proton.Conn) ([]exportedContact, error) {
	var all []exportedContact

	for page := 0; ; page++ {
		var res struct {
			Contacts []exportedContact
		}

		path := fmt.Sprintf("/contacts/v4/contacts/export?Page=%d&PageSize=%d", page, exportPageSize)

		if err := conn.Get(ctx, path, &res); err != nil {
			return nil, fmt.Errorf("exporting contacts: %w", err)
		}

		all = append(all, res.Contacts...)

		if len(res.Contacts) < exportPageSize {
			return all, nil
		}
	}
}

// uidOf returns the contact's vCard UID, falling back to Proton's own.
//
// A CardDAV client addresses contacts by UID, so one is required; contacts
// created in the Proton web app do carry one, but an imported vCard might not.
func uidOf(card vcard.Card, raw api.Contact) string {
	if uid := card.Value(vcard.FieldUID); uid != "" {
		return uid
	}

	if raw.UID != "" {
		return raw.UID
	}

	return raw.ID
}

// Put creates a contact, or replaces the one with the same UID. It reports
// whether the contact was created.
func Put(ctx context.Context, conn *proton.Conn, card vcard.Card) (id string, created bool, err error) {
	kr := conn.ContactWriteKeyRing()

	uid := card.Value(vcard.FieldUID)
	if uid == "" {
		return "", false, fmt.Errorf("contact has no UID")
	}

	cards, err := split(kr, card)
	if err != nil {
		return "", false, err
	}

	existing, err := findByUID(ctx, conn, uid)
	if err != nil {
		return "", false, err
	}

	if existing != nil {
		if _, err := conn.Client.UpdateContact(ctx, existing.ID, api.UpdateContactReq{Cards: cards}); err != nil {
			return "", false, fmt.Errorf("updating contact: %w", err)
		}

		return existing.ID, false, nil
	}

	res, err := conn.Client.CreateContacts(ctx, api.CreateContactsReq{
		Contacts:  []api.ContactCards{{Cards: cards}},
		Overwrite: overwriteContact,
		Labels:    labelsFor(card),
	})
	if err != nil {
		return "", false, fmt.Errorf("creating contact: %w", err)
	}

	if len(res) != 1 {
		return "", false, fmt.Errorf("expected one create response, got %d", len(res))
	}

	return res[0].Response.Contact.ID, true, nil
}

// Delete removes the contact with the given UID, reporting whether one was
// found.
func Delete(ctx context.Context, conn *proton.Conn, uid string) (bool, error) {
	existing, err := findByUID(ctx, conn, uid)
	if err != nil {
		return false, err
	}

	if existing == nil {
		return false, nil
	}

	if err := conn.Client.DeleteContacts(ctx, api.DeleteContactsReq{IDs: []string{existing.ID}}); err != nil {
		return false, fmt.Errorf("deleting contact: %w", err)
	}

	return true, nil
}

func findByUID(ctx context.Context, conn *proton.Conn, uid string) (*Contact, error) {
	all, _, err := List(ctx, conn)
	if err != nil {
		return nil, err
	}

	for i := range all {
		if all[i].UID == uid {
			return &all[i], nil
		}
	}

	return nil, nil
}

// split divides a vCard into the cards Proton stores.
func split(kr *crypto.KeyRing, card vcard.Card) (api.Cards, error) {
	card = clone(card)
	vcard.ToV4(card)
	groupEmails(card)

	signed := make(vcard.Card)
	encrypted := card

	// Before the signed pass, so that PRODID is still here to be copied.
	var clear vcard.Card

	if len(encrypted[vcard.FieldCategories]) > 0 {
		clear = make(vcard.Card)

		for _, name := range clearFields {
			if fields, ok := encrypted[name]; ok {
				clear[name] = fields
			}
		}

		// VERSION and PRODID are signed as well, and the pass below takes
		// them out of the encrypted card. CATEGORIES is in the clear card
		// alone, so it has to be removed here.
		delete(encrypted, vcard.FieldCategories)
	}

	for _, name := range signedFields {
		fields, ok := encrypted[name]
		if !ok {
			continue
		}

		signed[name] = fields

		// VERSION belongs to both cards: each is a standalone vCard.
		if name != vcard.FieldVersion {
			delete(encrypted, name)
		}
	}

	var cards api.Cards

	if len(clear) > 0 {
		card, err := encode(kr, clear, api.CardTypeClear)
		if err != nil {
			return nil, err
		}

		cards = append(cards, card)
	}

	if len(signed) > 0 {
		card, err := encode(kr, signed, api.CardTypeSigned)
		if err != nil {
			return nil, err
		}

		cards = append(cards, card)
	}

	if len(encrypted) > 0 {
		card, err := encode(kr, encrypted, api.CardTypeEncrypted|api.CardTypeSigned)
		if err != nil {
			return nil, err
		}

		cards = append(cards, card)
	}

	return cards, nil
}

// encode renders a vCard and wraps it in a Proton card of the given type.
func encode(kr *crypto.KeyRing, card vcard.Card, cardType api.CardType) (*api.Card, error) {
	var buf bytes.Buffer
	if err := vcard.NewEncoder(&buf).Encode(card); err != nil {
		return nil, fmt.Errorf("encoding vCard: %w", err)
	}

	out := &api.Card{Type: cardType}

	if cardType&api.CardTypeSigned != 0 {
		sig, err := kr.SignDetached(crypto.NewPlainMessageFromString(buf.String()))
		if err != nil {
			return nil, fmt.Errorf("signing card: %w", err)
		}

		if out.Signature, err = sig.GetArmored(); err != nil {
			return nil, fmt.Errorf("armouring signature: %w", err)
		}
	}

	if cardType&api.CardTypeEncrypted != 0 {
		enc, err := kr.Encrypt(crypto.NewPlainMessageFromString(buf.String()), nil)
		if err != nil {
			return nil, fmt.Errorf("encrypting card: %w", err)
		}

		if out.Data, err = enc.GetArmored(); err != nil {
			return nil, fmt.Errorf("armouring card: %w", err)
		}
	} else {
		out.Data = buf.String()
	}

	return out, nil
}

// groupEmails gives every address a vCard group.
//
// Proton attaches per-address encryption preferences as X-PM-* fields sharing
// the address's group, so an ungrouped address has nowhere to hang them.
func groupEmails(card vcard.Card) {
	next := 1

	used := make(map[string]bool)
	for _, field := range card[vcard.FieldEmail] {
		if field.Group != "" {
			used[field.Group] = true
		}
	}

	for _, field := range card[vcard.FieldEmail] {
		if field.Group != "" {
			continue
		}

		for {
			group := "item" + strconv.Itoa(next)
			next++

			if !used[group] {
				field.Group = group
				used[group] = true

				break
			}
		}
	}
}

// clone copies a card so splitting does not mutate the caller's.
func clone(card vcard.Card) vcard.Card {
	out := make(vcard.Card, len(card))

	for name, fields := range card {
		copied := make([]*vcard.Field, 0, len(fields))

		for _, f := range fields {
			field := *f
			copied = append(copied, &field)
		}

		out[name] = copied
	}

	return out
}

// String renders a contact as a vCard.
func (c Contact) String() string {
	var buf strings.Builder
	if err := vcard.NewEncoder(&buf).Encode(c.Card); err != nil {
		return ""
	}

	return buf.String()
}
