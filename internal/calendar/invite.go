package calendar

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/mail"
	"os"
	"strings"

	"github.com/ProtonMail/gluon/rfc822"
	api "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
	"github.com/emersion/go-ical"

	"github.com/develonrails/carbonate/internal/proton"
)

// iTIP methods, as RFC 5546 names them. carbonate sends only these two: an
// invitation, and word that it is off.
const (
	methodRequest = "REQUEST"
	methodCancel  = "CANCEL"
)

// calendarMIMEType is what marks the attachment as scheduling rather than an
// ICS file someone happened to send. Mail clients act on the method only when
// it is named here.
func calendarMIMEType(method string) rfc822.MIMEType {
	return rfc822.MIMEType("text/calendar; charset=utf-8; method=" + method)
}

// noInvitations turns the mail off.
//
// Sending is a side effect a CalDAV client never asked for, and one that
// reaches people who are not the user. An escape hatch costs a line and means
// nobody has to choose between carbonate and their guests' inboxes.
const noInvitations = "CARBONATE_NO_INVITATIONS"

// tellGuests mails an iTIP message to the addresses given.
//
// At Proton an invitation is mail, so this is the only thing that actually
// reaches a guest: the event in the calendar is invisible to anyone who does
// not already know it is there.
//
// The message is sent from the calendar member's own address, because that is
// who Proton lets us sign as and who the event names as organiser.
func tellGuests(ctx context.Context, conn *proton.Conn, keys *proton.CalendarKeys, event *ical.Event, method string, addresses []string) error {
	if len(addresses) == 0 || os.Getenv(noInvitations) != "" {
		return nil
	}

	body, err := itip(event, method, keys.Email)
	if err != nil {
		return err
	}

	draft, err := conn.Client.CreateDraft(ctx, keys.AddrKR, api.CreateDraftReq{
		Message: api.DraftTemplate{
			Subject:  subject(event, method),
			Sender:   &mail.Address{Address: keys.Email},
			ToList:   recipients(addresses),
			Body:     invitationText(event, method),
			MIMEType: rfc822.TextPlain,
		},
	})
	if err != nil {
		return fmt.Errorf("creating the invitation: %w", err)
	}

	attachment, err := conn.Client.UploadAttachment(ctx, keys.AddrKR, api.CreateAttachmentReq{
		MessageID:   draft.ID,
		Filename:    "invite.ics",
		MIMEType:    calendarMIMEType(method),
		Disposition: api.AttachmentDisposition,
		Body:        []byte(body),
	})
	if err != nil {
		return fmt.Errorf("attaching the invitation: %w", err)
	}

	// The upload encrypts the attachment to our own address key and hands
	// back the packet, so the session key has to be recovered before it can
	// be re-wrapped for each guest.
	packets, err := base64.StdEncoding.DecodeString(attachment.KeyPackets)
	if err != nil {
		return fmt.Errorf("decoding the attachment key packet: %w", err)
	}

	attachmentKey, err := keys.AddrKR.DecryptSessionKey(packets)
	if err != nil {
		return fmt.Errorf("decrypting the attachment session key: %w", err)
	}

	req, err := packages(ctx, conn, keys, invitationText(event, method), addresses, map[string]*crypto.SessionKey{attachment.ID: attachmentKey})
	if err != nil {
		return err
	}

	if _, err := conn.Client.SendDraft(ctx, draft.ID, req); err != nil {
		return fmt.Errorf("sending the invitation: %w", err)
	}

	return nil
}

// packages sorts the guests by how their mail has to be protected.
//
// A guest on Proton gets the message encrypted to their address key. Everyone
// else gets it in the clear, which is what mail to a stranger has always been
// — carbonate has no key of theirs and no way to agree one.
//
// The two go in separate packages rather than one. A clear recipient makes the
// body's session key readable by anyone holding the message, and there is no
// reason to hand that to Proton for guests who did not need it.
func packages(ctx context.Context, conn *proton.Conn, keys *proton.CalendarKeys, body string, addresses []string, attachmentKeys map[string]*crypto.SessionKey) (api.SendDraftReq, error) {
	internal := make(map[string]api.SendPreferences)
	external := make(map[string]api.SendPreferences)

	for _, address := range addresses {
		kr, err := conn.PublicKeyRing(ctx, address)
		if err != nil {
			// Same bargain as handing out the event's session key: one guest
			// Proton will not answer for is not a reason to tell nobody.
			fmt.Fprintf(os.Stderr, "carbonate: could not look up keys for %s, who will be mailed in the clear: %v\n", address, err)

			kr = nil
		}

		if kr != nil {
			internal[address] = api.SendPreferences{
				Encrypt:          true,
				PubKey:           kr,
				SignatureType:    api.DetachedSignature,
				EncryptionScheme: api.InternalScheme,
				MIMEType:         rfc822.TextPlain,
			}

			continue
		}

		external[address] = api.SendPreferences{
			SignatureType:    api.NoSignature,
			EncryptionScheme: api.ClearScheme,
			MIMEType:         rfc822.TextPlain,
		}
	}

	var req api.SendDraftReq

	for _, group := range []map[string]api.SendPreferences{internal, external} {
		if len(group) == 0 {
			continue
		}

		if err := req.AddTextPackage(keys.AddrKR, body, rfc822.TextPlain, group, attachmentKeys); err != nil {
			return api.SendDraftReq{}, fmt.Errorf("preparing the invitation: %w", err)
		}
	}

	return req, nil
}

func recipients(addresses []string) []*mail.Address {
	out := make([]*mail.Address, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, &mail.Address{Address: address})
	}

	return out
}

// itip renders the event as a scheduling message.
//
// METHOD is what separates an invitation from a copy of an event: the same
// VEVENT with METHOD:REQUEST asks people to come, and with METHOD:CANCEL tells
// them it is off. An organiser is required either way, so the calendar
// member's address stands in when the client named nobody.
func itip(event *ical.Event, method, organiser string) (string, error) {
	out := ical.NewEvent()

	for name, props := range event.Props {
		// X-PM-TOKEN rides along as a parameter on each ATTENDEE, put there
		// for Proton's benefit. It is how Proton tracks a reply without
		// learning who was invited, and means nothing to a guest's mail
		// client — sending it names our storage in someone else's inbox.
		//
		// Copied rather than stripped in place: the event is still on its way
		// to Proton, which does want it.
		copied := make([]ical.Prop, len(props))

		for i, prop := range props {
			copied[i] = prop

			if prop.Params == nil || prop.Params.Get("X-PM-TOKEN") == "" {
				continue
			}

			copied[i].Params = ical.Params{}

			for key, values := range prop.Params {
				if !strings.EqualFold(key, "X-PM-TOKEN") {
					copied[i].Params[key] = values
				}
			}
		}

		out.Props[name] = copied
	}

	if p := out.Props.Get("ORGANIZER"); p == nil || strings.TrimSpace(p.Value) == "" {
		out.Props.Set(&ical.Prop{Name: "ORGANIZER", Value: "mailto:" + organiser})
	}

	// A cancellation that does not say the event is off leaves the guest
	// looking at an invitation they have already been told to ignore.
	if method == methodCancel {
		out.Props.Set(&ical.Prop{Name: "STATUS", Value: "CANCELLED"})
	}

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//carbonate//EN")
	cal.Props.Set(&ical.Prop{Name: "METHOD", Value: method})
	cal.Children = append(cal.Children, out.Component)

	var buf strings.Builder
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return "", fmt.Errorf("rendering the invitation: %w", err)
	}

	return buf.String(), nil
}

// summaryOf is the event's title, or a stand-in when it has none. An untitled
// event is legal and still has to be announced as something.
func summaryOf(event *ical.Event) string {
	if p := event.Props.Get("SUMMARY"); p != nil && strings.TrimSpace(p.Value) != "" {
		return strings.TrimSpace(p.Value)
	}

	return "(no title)"
}

func subject(event *ical.Event, method string) string {
	if method == methodCancel {
		return "Cancelled: " + summaryOf(event)
	}

	return "Invitation: " + summaryOf(event)
}

// invitationText is what a mail client shows when it does not understand the
// attachment. Clients that do understand it ignore this entirely.
func invitationText(event *ical.Event, method string) string {
	if method == methodCancel {
		return summaryOf(event) + " has been cancelled."
	}

	return "You have been invited to " + summaryOf(event) + "."
}

// newGuests names the attendees this write invites who were not invited
// before, so that a re-PUT of an unchanged event mails nobody.
//
// Proton's clear attendee list holds tokens rather than addresses, which is
// exactly what is needed here: the token is derived from the address, so it
// answers "was this person already here" without decrypting anything.
//
// We are never one of them. Clients routinely list the organiser among the
// attendees, and mailing yourself an invitation to your own meeting is noise.
func newGuests(event *ical.Event, existing *rawEvent, self string) []string {
	fields := event.Props["ATTENDEE"]
	if len(fields) == 0 {
		return nil
	}

	uid := ""
	if p := event.Props.Get("UID"); p != nil {
		uid = strings.TrimSpace(p.Value)
	}

	invited := make(map[string]bool)

	if existing != nil {
		for _, a := range existing.Attendees {
			invited[a.Token] = true
		}
	}

	seen := make(map[string]bool, len(fields))

	var out []string

	for i := range fields {
		address := normaliseAddress(fields[i].Value)

		if invited[attendeeToken(uid, fields[i].Value)] || address == normaliseAddress(self) || seen[address] {
			continue
		}

		seen[address] = true

		out = append(out, address)
	}

	return out
}
