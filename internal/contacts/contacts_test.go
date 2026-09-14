package contacts

import (
	"strings"
	"testing"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/emersion/go-vcard"
)

func testKeyRing(t *testing.T) *crypto.KeyRing {
	t.Helper()

	key, err := crypto.GenerateKey("Test", "test@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	kr, err := crypto.NewKeyRing(key)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	return kr
}

func testCard() vcard.Card {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "person@example.com")
	card.SetValue(vcard.FieldFormattedName, "Jan Jansen")
	card.SetValue(vcard.FieldName, "Jansen;Jan;;;")
	card.SetValue(vcard.FieldEmail, "jan@example.com")
	card.SetValue(vcard.FieldTelephone, "+31612345678")
	card.SetValue(vcard.FieldNote, "Secret note")

	return card
}

// Splitting and merging are inverse operations; anything lost between them is
// data the user silently never gets back.
func TestSplitRoundTrips(t *testing.T) {
	kr := testKeyRing(t)

	cards, err := split(kr, testCard())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	merged, err := cards.Merge(kr)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	for field, want := range map[string]string{
		vcard.FieldUID:           "person@example.com",
		vcard.FieldFormattedName: "Jan Jansen",
		vcard.FieldName:          "Jansen;Jan;;;",
		vcard.FieldEmail:         "jan@example.com",
		vcard.FieldTelephone:     "+31612345678",
		vcard.FieldNote:          "Secret note",
	} {
		if got := merged.Value(field); got != want {
			t.Errorf("%s = %q after a round trip, want %q", field, got, want)
		}
	}
}

// Everything Proton does not need to read must be encrypted. A note or a
// phone number appearing in the signed card would be readable on the server.
func TestPrivateFieldsAreEncrypted(t *testing.T) {
	kr := testKeyRing(t)

	cards, err := split(kr, testCard())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	signed, ok := cards.Get(api.CardTypeSigned)
	if !ok {
		t.Fatal("no signed card was produced")
	}

	for _, secret := range []string{"Secret note", "+31612345678", "Jansen;Jan"} {
		if strings.Contains(signed.Data, secret) {
			t.Errorf("signed card exposes %q in the clear:\n%s", secret, signed.Data)
		}
	}

	// And the fields Proton does need must be there.
	for _, want := range []string{"Jan Jansen", "jan@example.com", "person@example.com"} {
		if !strings.Contains(signed.Data, want) {
			t.Errorf("signed card is missing %q:\n%s", want, signed.Data)
		}
	}
}

func TestEncryptedCardIsNotReadable(t *testing.T) {
	kr := testKeyRing(t)

	cards, err := split(kr, testCard())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	encrypted, ok := cards.Get(api.CardTypeEncrypted | api.CardTypeSigned)
	if !ok {
		t.Fatal("no encrypted card was produced")
	}

	if strings.Contains(encrypted.Data, "Secret note") {
		t.Error("the encrypted card is not actually encrypted")
	}

	if !strings.HasPrefix(encrypted.Data, "-----BEGIN PGP MESSAGE-----") {
		t.Errorf("encrypted card is not an armoured PGP message: %.40q", encrypted.Data)
	}
}

// Both cards are standalone vCards, so each needs its own VERSION.
func TestBothCardsCarryVersion(t *testing.T) {
	kr := testKeyRing(t)

	cards, err := split(kr, testCard())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	signed, _ := cards.Get(api.CardTypeSigned)
	if !strings.Contains(signed.Data, "VERSION:4.0") {
		t.Error("signed card has no VERSION")
	}

	encrypted, _ := cards.Get(api.CardTypeEncrypted | api.CardTypeSigned)

	only := api.Cards{encrypted}

	decoded, err := only.Merge(kr)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if decoded.Value(vcard.FieldVersion) == "" {
		t.Error("encrypted card has no VERSION")
	}
}

// Proton hangs per-address encryption preferences off the address's group, so
// an ungrouped address has nowhere to carry them.
func TestGroupEmailsAssignsDistinctGroups(t *testing.T) {
	card := make(vcard.Card)
	card.AddValue(vcard.FieldEmail, "one@example.com")
	card.AddValue(vcard.FieldEmail, "two@example.com")

	groupEmails(card)

	groups := make(map[string]bool)
	for _, field := range card[vcard.FieldEmail] {
		if field.Group == "" {
			t.Error("an address was left without a group")
		}

		if groups[field.Group] {
			t.Errorf("group %q was reused", field.Group)
		}

		groups[field.Group] = true
	}
}

// A client may send addresses that already carry groups; those must survive,
// and a generated group must not collide with them.
func TestGroupEmailsKeepsExistingGroups(t *testing.T) {
	card := make(vcard.Card)
	card.AddValue(vcard.FieldEmail, "one@example.com")
	card[vcard.FieldEmail][0].Group = "item1"
	card.AddValue(vcard.FieldEmail, "two@example.com")

	groupEmails(card)

	if card[vcard.FieldEmail][0].Group != "item1" {
		t.Errorf("existing group was changed to %q", card[vcard.FieldEmail][0].Group)
	}

	if card[vcard.FieldEmail][1].Group == "item1" {
		t.Error("generated group collided with an existing one")
	}
}

// split must not mutate the caller's card, or a retry would send something
// different from what was asked for.
func TestSplitDoesNotMutateInput(t *testing.T) {
	kr := testKeyRing(t)
	card := testCard()

	if _, err := split(kr, card); err != nil {
		t.Fatalf("split: %v", err)
	}

	for _, field := range []string{vcard.FieldFormattedName, vcard.FieldNote, vcard.FieldTelephone} {
		if card.Value(field) == "" {
			t.Errorf("split removed %s from the caller's card", field)
		}
	}
}
