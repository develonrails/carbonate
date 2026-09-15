package carddav

import (
	"context"
	"time"

	"github.com/emersion/go-vcard"

	"github.com/develonrails/carbonate/internal/contacts"
)

// fakeContacts stands in for Proton, recording what it was asked to do.
type fakeContacts struct {
	people []contacts.Contact

	listCalls int
	put       []vcard.Card
	deleted   []string

	err error
}

func newFakeContacts() *fakeContacts {
	card := make(vcard.Card)
	card.SetValue(vcard.FieldVersion, "4.0")
	card.SetValue(vcard.FieldUID, "person@example.com")
	card.SetValue(vcard.FieldFormattedName, "Jan Jansen")

	return &fakeContacts{
		people: []contacts.Contact{{
			ID:       "contact-1",
			UID:      "person@example.com",
			Modified: time.Unix(1000, 0),
			Card:     card,
		}},
	}
}

func (f *fakeContacts) Contacts(context.Context) ([]contacts.Contact, error) {
	f.listCalls++

	if f.err != nil {
		return nil, f.err
	}

	return f.people, nil
}

func (f *fakeContacts) Put(_ context.Context, card vcard.Card) (string, bool, error) {
	f.put = append(f.put, card)

	if f.err != nil {
		return "", false, f.err
	}

	f.people = append(f.people, contacts.Contact{
		ID:       "new",
		UID:      card.Value(vcard.FieldUID),
		Modified: time.Unix(2000, 0),
		Card:     card,
	})

	return "new", true, nil
}

func (f *fakeContacts) Delete(_ context.Context, uid string) (bool, error) {
	f.deleted = append(f.deleted, uid)

	if f.err != nil {
		return false, f.err
	}

	kept := make([]contacts.Contact, 0, len(f.people))
	found := false

	for _, c := range f.people {
		if c.UID == uid {
			found = true

			continue
		}

		kept = append(kept, c)
	}

	f.people = kept

	return found, nil
}
