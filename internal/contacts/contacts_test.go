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

// A pinned key and the per-address encryption settings must land in the
// signed card.
//
// Proton reads them from there and nowhere else, so an encrypted one is not
// a slower path to the same place — it is the settings silently ceasing to
// apply. carbonate merges every card on read, so these fields come back from
// Proton looking perfectly intact and are handed to the client, which means
// a misplaced write looks correct from this side and is only visible as mail
// that stops being encrypted.
func TestKeyAndEncryptionSettingsAreSigned(t *testing.T) {
	kr := testKeyRing(t)

	card := testCard()
	card[vcard.FieldEmail][0].Group = "item1"

	for name, value := range map[string]string{
		vcard.FieldKey:              "data:application/pgp-keys;base64,AAAA",
		api.FieldPMEncrypt:          "true",
		api.FieldPMEncryptUntrusted: "true",
		api.FieldPMSign:             "true",
		api.FieldPMScheme:           "pgp-mime",
		api.FieldPMMIMEType:         "text/plain",
		fieldPMTLS:                  "true",
	} {
		card.Add(name, &vcard.Field{Value: value, Group: "item1"})
	}

	cards, err := split(kr, card)
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	signed, ok := cards.Get(api.CardTypeSigned)
	if !ok {
		t.Fatal("no signed card was produced")
	}

	for _, name := range []string{
		vcard.FieldKey,
		api.FieldPMEncrypt,
		api.FieldPMEncryptUntrusted,
		api.FieldPMSign,
		api.FieldPMScheme,
		api.FieldPMMIMEType,
		fieldPMTLS,
	} {
		if !strings.Contains(signed.Data, name) {
			t.Errorf("%s is not in the signed card, so Proton will not act on it:\n%s", name, signed.Data)
		}
	}

	// The group is what ties them to the address, so it has to survive too.
	if !strings.Contains(signed.Data, "item1.EMAIL") {
		t.Errorf("the address lost its group, leaving its settings attached to nothing:\n%s", signed.Data)
	}
}

// Categories go in a cleartext card, because the server files the contact
// under those groups and cannot read an encrypted one.
func TestCategoriesGoInAClearCard(t *testing.T) {
	kr := testKeyRing(t)

	card := testCard()
	card.AddValue(vcard.FieldCategories, "Work")

	cards, err := split(kr, card)
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	clear, ok := cards.Get(api.CardTypeClear)
	if !ok {
		t.Fatal("a contact with categories produced no cleartext card")
	}

	if !strings.Contains(clear.Data, "CATEGORIES:Work") {
		t.Errorf("the cleartext card does not carry the categories:\n%s", clear.Data)
	}

	if clear.Signature != "" {
		t.Error("the cleartext card was signed; Proton sends no signature with one")
	}

	// And nowhere else, or the merged contact would list the group twice.
	encrypted, ok := cards.Get(api.CardTypeEncrypted | api.CardTypeSigned)
	if ok && strings.Contains(encrypted.Data, "CATEGORIES") {
		t.Error("categories were left in the encrypted card as well")
	}

	merged, err := cards.Merge(kr)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if got := merged[vcard.FieldCategories]; len(got) != 1 {
		t.Errorf("round trip produced %d CATEGORIES, want 1", len(got))
	}
}

// A contact without categories gets no cleartext card at all: Proton makes
// one for categories and for nothing else, and an empty one would publish
// PRODID for no reason.
func TestNoCategoriesMeansNoClearCard(t *testing.T) {
	kr := testKeyRing(t)

	cards, err := split(kr, testCard())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	if _, ok := cards.Get(api.CardTypeClear); ok {
		t.Error("a contact with no categories still produced a cleartext card")
	}
}

// The Labels flag is what makes the server act on the categories. Without it
// the cleartext card is sent and ignored, which looks like success.
func TestLabelsFollowCategories(t *testing.T) {
	if got := labelsFor(testCard()); got != labelsIgnore {
		t.Errorf("labelsFor without categories = %d, want %d", got, labelsIgnore)
	}

	card := testCard()
	card.AddValue(vcard.FieldCategories, "Work")

	if got := labelsFor(card); got != labelsInclude {
		t.Errorf("labelsFor with categories = %d, want %d", got, labelsInclude)
	}
}
