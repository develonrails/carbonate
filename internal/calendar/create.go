package calendar

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/develonrails/carbonate/internal/proton"
)

// palette is the set of colours Proton Calendar offers. The web app never
// sends anything else, so neither does carbonate: a client's colour is moved
// to the nearest of these rather than trusted to be accepted as it is.
var palette = []string{
	"#8080FF", "#DB60D6", "#EC3E7C", "#F78400", "#936D58",
	"#5252CC", "#A839A4", "#BA1E55", "#C44800", "#54473F",
	"#415DF0", "#179FD9", "#1DA583", "#3CBB3A", "#B4A40E",
	"#273EB2", "#0A77A6", "#0F735A", "#258723", "#807304",
}

// DefaultColor is used when a client names no colour, or one that cannot be
// read.
const DefaultColor = "#8080FF"

// ClosestColor returns the Proton colour nearest to a client's.
//
// Clients write colours as #RGB, #RRGGBB or, in Apple's case, #RRGGBBAA. The
// alpha is dropped: Proton has nowhere to keep it.
func ClosestColor(color string) string {
	r, g, b, ok := parseColor(color)
	if !ok {
		return DefaultColor
	}

	best, bestDistance := DefaultColor, -1

	for _, candidate := range palette {
		cr, cg, cb, _ := parseColor(candidate)

		distance := (r-cr)*(r-cr) + (g-cg)*(g-cg) + (b-cb)*(b-cb)
		if bestDistance < 0 || distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}

	return best
}

func parseColor(color string) (r, g, b int, ok bool) {
	hex := strings.TrimPrefix(strings.TrimSpace(color), "#")

	switch len(hex) {
	case 3:
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	case 6:
	case 8:
		hex = hex[:6]
	default:
		return 0, 0, 0, false
	}

	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}

	return int(value >> 16), int(value >> 8 & 0xff), int(value & 0xff), true
}

// keySetup is what Proton asks for when a calendar is given its key.
//
// A calendar key is locked with a random passphrase, and the passphrase is
// what members are actually given: encrypted to the member's address key, and
// signed with it so that nobody can swap in a passphrase of their own.
type keySetup struct {
	AddressID  string
	Signature  string
	PrivateKey string
	Passphrase keySetupPassphrase
}

type keySetupPassphrase struct {
	DataPacket string
	KeyPacket  string
}

// newKeySetup generates a calendar key and wraps its passphrase for the
// address.
//
// This follows generateCalendarKeyPayload in Proton's web client step for
// step, because the server stores what it is sent and every other client then
// has to be able to open it.
func newKeySetup(addressID string, addrKR *crypto.KeyRing) (keySetup, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return keySetup{}, fmt.Errorf("generating a passphrase: %w", err)
	}

	passphrase := base64.StdEncoding.EncodeToString(random)

	key, err := crypto.GenerateKey("Calendar key", "", "x25519", 0)
	if err != nil {
		return keySetup{}, fmt.Errorf("generating the calendar key: %w", err)
	}

	locked, err := key.Lock([]byte(passphrase))
	if err != nil {
		return keySetup{}, fmt.Errorf("locking the calendar key: %w", err)
	}

	armored, err := locked.Armor()
	if err != nil {
		return keySetup{}, fmt.Errorf("serialising the calendar key: %w", err)
	}

	// An address can hold several keys. Encrypting to the ring would write a
	// key packet for each, and Proton takes exactly one, for the primary.
	primary, err := addrKR.FirstKey()
	if err != nil {
		return keySetup{}, fmt.Errorf("finding the primary address key: %w", err)
	}

	message := crypto.NewPlainMessageFromString(passphrase)

	encrypted, err := primary.Encrypt(message, nil)
	if err != nil {
		return keySetup{}, fmt.Errorf("encrypting the passphrase: %w", err)
	}

	split, err := encrypted.SplitMessage()
	if err != nil {
		return keySetup{}, fmt.Errorf("splitting the encrypted passphrase: %w", err)
	}

	signature, err := primary.SignDetached(message)
	if err != nil {
		return keySetup{}, fmt.Errorf("signing the passphrase: %w", err)
	}

	armoredSignature, err := signature.GetArmored()
	if err != nil {
		return keySetup{}, fmt.Errorf("serialising the signature: %w", err)
	}

	setup := keySetup{
		AddressID:  addressID,
		Signature:  armoredSignature,
		PrivateKey: armored,
		Passphrase: keySetupPassphrase{
			DataPacket: base64.StdEncoding.EncodeToString(split.GetBinaryDataPacket()),
			KeyPacket:  base64.StdEncoding.EncodeToString(split.GetBinaryKeyPacket()),
		},
	}

	// Proton keeps whatever arrives. A key nobody can open is a calendar
	// nobody can use, and it would be found out by the first event written
	// to it — so open it here, the way a reader will, before it is sent.
	if err := setup.check(addrKR); err != nil {
		return keySetup{}, fmt.Errorf("the generated calendar key does not open: %w", err)
	}

	return setup, nil
}

// check opens the setup the way a client reading the calendar would: decrypt
// the passphrase with the address key, verify its signature, and unlock the
// calendar key with it.
func (s keySetup) check(addrKR *crypto.KeyRing) error {
	keyPacket, err := base64.StdEncoding.DecodeString(s.Passphrase.KeyPacket)
	if err != nil {
		return fmt.Errorf("key packet: %w", err)
	}

	dataPacket, err := base64.StdEncoding.DecodeString(s.Passphrase.DataPacket)
	if err != nil {
		return fmt.Errorf("data packet: %w", err)
	}

	decrypted, err := addrKR.Decrypt(crypto.NewPGPSplitMessage(keyPacket, dataPacket).GetPGPMessage(), nil, 0)
	if err != nil {
		return fmt.Errorf("decrypting the passphrase: %w", err)
	}

	signature, err := crypto.NewPGPSignatureFromArmored(s.Signature)
	if err != nil {
		return fmt.Errorf("reading the signature: %w", err)
	}

	if err := addrKR.VerifyDetached(decrypted, signature, crypto.GetUnixTime()); err != nil {
		return fmt.Errorf("verifying the passphrase signature: %w", err)
	}

	key, err := crypto.NewKeyFromArmored(s.PrivateKey)
	if err != nil {
		return fmt.Errorf("reading the calendar key: %w", err)
	}

	if _, err := key.Unlock(decrypted.GetBinary()); err != nil {
		return fmt.Errorf("unlocking the calendar key: %w", err)
	}

	return nil
}

// primaryAddress returns the address a new calendar belongs to, and its keys.
func primaryAddress(ctx context.Context, conn *proton.Conn) (api.Address, *crypto.KeyRing, error) {
	addresses, err := conn.Client.GetAddresses(ctx)
	if err != nil {
		return api.Address{}, nil, fmt.Errorf("fetching addresses: %w", err)
	}

	// Addresses come back ordered; the first enabled one is the primary.
	for _, addr := range addresses {
		if addr.Status != api.AddressStatusEnabled {
			continue
		}

		kr, err := conn.AddressKeyRing(addr)
		if err != nil {
			return api.Address{}, nil, err
		}

		return addr, kr, nil
	}

	return api.Address{}, nil, errors.New("account has no enabled address")
}

// Create makes a calendar and gives it a key, returning its ID.
//
// These are two requests, as they are in the web app: the calendar is created
// empty, and only then given a key. If the second fails the empty calendar
// stays, because taking it back out is not open to carbonate: Proton answers
// a DELETE with 403 and code 9101, a scope this session does not hold. The
// web app finishes setting up a calendar it finds without a key
// (setupCalendarKeys), so the error says to go there.
func Create(ctx context.Context, conn *proton.Conn, name, color string) (string, error) {
	addr, addrKR, err := primaryAddress(ctx, conn)
	if err != nil {
		return "", err
	}

	// Made before the calendar is, so that a key that cannot be generated
	// leaves nothing behind.
	setup, err := newKeySetup(addr.ID, addrKR)
	if err != nil {
		return "", err
	}

	body := struct {
		Name        string
		Description string
		Color       string
		Display     int
		AddressID   string
	}{Name: name, Color: ClosestColor(color), Display: 1, AddressID: addr.ID}

	var created struct {
		Calendar struct{ ID string }
	}

	if err := conn.Post(ctx, "/calendar/v1", body, &created); err != nil {
		return "", fmt.Errorf("creating the calendar: %w", err)
	}

	id := created.Calendar.ID
	if id == "" {
		return "", errors.New("creating the calendar: Proton returned no calendar ID")
	}

	if err := conn.Post(ctx, "/calendar/v1/"+id+"/keys", setup, nil); err != nil {
		return "", fmt.Errorf("the calendar was created but could not be given a key, so nothing can be written to it; open Proton Calendar on the web to finish setting it up, or to delete it: %w", err)
	}

	return id, nil
}

// Update renames or recolours a calendar. A nil argument leaves that detail
// as it is.
//
// Both live on our membership of the calendar rather than on the calendar:
// each member of a shared calendar names and colours it for themselves.
func Update(ctx context.Context, conn *proton.Conn, calendarID string, name, color *string) error {
	if name == nil && color == nil {
		return nil
	}

	keys, err := conn.CalendarKeys(ctx, calendarID)
	if err != nil {
		return err
	}

	body := make(map[string]string, 2)

	if name != nil {
		body["Name"] = *name
	}

	if color != nil {
		body["Color"] = ClosestColor(*color)
	}

	path := fmt.Sprintf("/calendar/v1/%s/members/%s", calendarID, keys.MemberID)

	if err := conn.Put(ctx, path, body, nil); err != nil {
		return fmt.Errorf("updating the calendar: %w", err)
	}

	return nil
}
