package calendar

import (
	"bytes"
	"encoding/base64"
	"testing"

	pgppacket "github.com/ProtonMail/go-crypto/openpgp/packet"
	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
)

func addressKeyRing(t *testing.T, addresses ...string) *crypto.KeyRing {
	t.Helper()

	kr, err := crypto.NewKeyRing(nil)
	if err != nil {
		t.Fatalf("NewKeyRing: %v", err)
	}

	for _, address := range addresses {
		key, err := crypto.GenerateKey(address, address, "x25519", 0)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}

		if err := kr.AddKey(key); err != nil {
			t.Fatalf("AddKey: %v", err)
		}
	}

	return kr
}

// stored turns a setup into what Proton hands back for it: the two packets
// joined into one armored message, beside the signature.
func stored(t *testing.T, setup keySetup) api.CalendarPassphrase {
	t.Helper()

	keyPacket, err := base64.StdEncoding.DecodeString(setup.Passphrase.KeyPacket)
	if err != nil {
		t.Fatalf("key packet: %v", err)
	}

	dataPacket, err := base64.StdEncoding.DecodeString(setup.Passphrase.DataPacket)
	if err != nil {
		t.Fatalf("data packet: %v", err)
	}

	armored, err := crypto.NewPGPSplitMessage(keyPacket, dataPacket).GetPGPMessage().GetArmored()
	if err != nil {
		t.Fatalf("armoring the passphrase: %v", err)
	}

	return api.CalendarPassphrase{MemberPassphrases: []api.MemberPassphrase{{
		MemberID:   "member",
		Passphrase: armored,
		Signature:  setup.Signature,
	}}}
}

// The question Proton's other clients will ask: given only what was sent, and
// the address key, does the calendar key open? This reads the setup with the
// same code carbonate reads every existing calendar with, rather than with
// the code that wrote it.
func TestKeySetupOpensTheWayACalendarIsRead(t *testing.T) {
	addrKR := addressKeyRing(t, "alice@example.com")

	setup, err := newKeySetup("address-id", addrKR)
	if err != nil {
		t.Fatalf("newKeySetup: %v", err)
	}

	if setup.AddressID != "address-id" {
		t.Errorf("AddressID = %q", setup.AddressID)
	}

	passphrase, err := stored(t, setup).Decrypt("member", addrKR)
	if err != nil {
		t.Fatalf("decrypting the passphrase as a reader would: %v", err)
	}

	calKR, err := api.CalendarKeys{{PrivateKey: setup.PrivateKey}}.Unlock(passphrase)
	if err != nil {
		t.Fatalf("unlocking the calendar key: %v", err)
	}

	if calKR.CountEntities() != 1 {
		t.Fatalf("the calendar keyring holds %d keys, want 1", calKR.CountEntities())
	}

	// A key that unlocks but cannot be used would still be a dead calendar.
	message := crypto.NewPlainMessageFromString("BEGIN:VCALENDAR")

	encrypted, err := calKR.Encrypt(message, nil)
	if err != nil {
		t.Fatalf("encrypting with the calendar key: %v", err)
	}

	decrypted, err := calKR.Decrypt(encrypted, nil, 0)
	if err != nil {
		t.Fatalf("decrypting with the calendar key: %v", err)
	}

	if decrypted.GetString() != "BEGIN:VCALENDAR" {
		t.Errorf("round trip gave %q", decrypted.GetString())
	}
}

// Proton takes one key packet, for the address's primary key. Encrypting to
// the whole ring would write one per key.
func TestKeySetupEncryptsToThePrimaryKeyOnly(t *testing.T) {
	addrKR := addressKeyRing(t, "primary@example.com", "older@example.com")

	setup, err := newKeySetup("address-id", addrKR)
	if err != nil {
		t.Fatalf("newKeySetup: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(setup.Passphrase.KeyPacket)
	if err != nil {
		t.Fatalf("key packet: %v", err)
	}

	reader := pgppacket.NewReader(bytes.NewReader(raw))
	count := 0

	for {
		p, err := reader.Next()
		if err != nil {
			break
		}

		if _, ok := p.(*pgppacket.EncryptedKey); !ok {
			t.Fatalf("the key packet holds a %T", p)
		}

		count++
	}

	if count != 1 {
		t.Fatalf("got %d key packets, want 1", count)
	}

	primary, err := addrKR.FirstKey()
	if err != nil {
		t.Fatalf("FirstKey: %v", err)
	}

	if _, err := stored(t, setup).Decrypt("member", primary); err != nil {
		t.Errorf("the primary key alone cannot open the passphrase: %v", err)
	}
}

// Every setup has its own key and its own passphrase; two calendars sharing
// either would share their contents.
func TestKeySetupsAreIndependent(t *testing.T) {
	addrKR := addressKeyRing(t, "alice@example.com")

	first, err := newKeySetup("address-id", addrKR)
	if err != nil {
		t.Fatalf("newKeySetup: %v", err)
	}

	second, err := newKeySetup("address-id", addrKR)
	if err != nil {
		t.Fatalf("newKeySetup: %v", err)
	}

	if first.PrivateKey == second.PrivateKey {
		t.Error("two setups share a calendar key")
	}

	a, err := stored(t, first).Decrypt("member", addrKR)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	b, err := stored(t, second).Decrypt("member", addrKR)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	if bytes.Equal(a, b) {
		t.Error("two setups share a passphrase")
	}
}

func TestClosestColor(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"a Proton colour is kept", "#EC3E7C", "#EC3E7C"},
		{"case does not matter", "#ec3e7c", "#EC3E7C"},
		{"Apple's alpha is dropped", "#1BADF8FF", "#179FD9"},
		{"short form", "#F00", "#C44800"},
		{"nothing given", "", DefaultColor},
		{"not a colour", "blue", DefaultColor},
		{"wrong length", "#12345", DefaultColor},
	}

	for _, c := range cases {
		if got := ClosestColor(c.in); got != c.want {
			t.Errorf("%s: ClosestColor(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
