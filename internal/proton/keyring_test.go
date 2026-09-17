package proton_test

import (
	"context"
	"testing"

	"github.com/develonrails/carbonate/internal/proton"
)

// Proton does not settle on one key for contacts: the web app encrypts to the
// account's user key, while the address key is what carbonate was built
// against. Reading with only the address key is not a contact that arrives
// incomplete — it is a hard decryption failure, and one such card used to
// take the entire address book down with it.
func TestContactKeyRingHoldsBothKinds(t *testing.T) {
	newTestServer(t)

	ctx := context.Background()

	conn, err := proton.Resume(ctx, &fakeStore{sess: login(t)})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	defer conn.Close()

	addrKR, err := conn.PrimaryAddressKeyRing(ctx)
	if err != nil {
		t.Fatalf("PrimaryAddressKeyRing: %v", err)
	}

	contactKR, err := conn.ContactKeyRing(ctx)
	if err != nil {
		t.Fatalf("ContactKeyRing: %v", err)
	}

	want := len(addrKR.GetKeys()) + len(conn.UserKR.GetKeys())

	if got := len(contactKR.GetKeys()); got != want {
		t.Errorf("the contact keyring holds %d keys, want %d (address %d + user %d)",
			got, want, len(addrKR.GetKeys()), len(conn.UserKR.GetKeys()))
	}

	if contactKR.CountDecryptionEntities() == 0 {
		t.Error("the contact keyring decrypts nothing")
	}
}
