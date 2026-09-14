# Architecture

## Intended dependencies

None of these are wired up yet; the scaffold is stdlib-only on purpose.

| Concern | Library | Maintained by |
|---|---|---|
| Proton API client, event loop | `github.com/ProtonMail/go-proton-api` | Proton |
| SRP authentication | `github.com/ProtonMail/go-srp` | Proton |
| OpenPGP | `github.com/ProtonMail/gopenpgp/v2` | Proton |
| CalDAV/CardDAV server | `github.com/emersion/go-webdav` | emersion |
| iCalendar / vCard parsing | `github.com/emersion/go-ical`, `go-vcard` | emersion |

`go-webdav` exposes CalDAV and CardDAV as backend interfaces — we implement
`GetCalendarObject`, `PutCalendarObject` and friends, and it handles the
PROPFIND/REPORT XML. protoxide vendors go-webdav with an additive patch to emit
the CalendarServer `getctag` property, which lets clients detect changes without
rescanning; we want that patch too.

## Packages

- `internal/proton` — Proton's private API: auth, key unlocking, event loop.
- `internal/session` — credential storage and refresh. Encrypted at rest with a
  locally generated bridge password. **Transparent token refresh lives here.**
- `internal/cache` — decrypted events and contacts on disk, keyed by a URL-safe
  path token so DAV requests resolve to Proton IDs in O(1). Files are 0600.
- `internal/caldav` — CalDAV backend over Proton Calendar.
- `internal/carddav` — CardDAV backend over Proton Contacts.

## Proton's data model

### Contacts

Already vCards. Each contact is a set of cards, one per protection level:

| Type | Meaning |
|---|---|
| 0 | cleartext |
| 1 | encrypted, unsigned *(legacy — protoxide fixed a bug where these were served as raw ciphertext)* |
| 2 | signed, not encrypted |
| 3 | encrypted and signed |

Read: decrypt each card with the address key, merge properties into one vCard.
Write: split properties back across the buckets, sign, encrypt.

### Calendar events

Each calendar has its own key, whose passphrase is encrypted to an address key.
An event is iCalendar split across parts, each with a signed and an encrypted
section:

- `SharedEvents` — the fields every attendee sees (DTSTART, DTEND, RRULE, …)
- `CalendarEvents` — organiser-only fields
- `AttendeesEvents` — attendee list
- `PersonalEvents` — per-user state such as alarms

Read: decrypt each part, reassemble into one VEVENT.
Write: decide which property belongs in which part, generate session keys, sign.
**Getting this split wrong corrupts data that the Proton web app then shows
incorrectly.** This is the highest-risk area in the project.

## Sync

Proton exposes an event-loop endpoint returning deltas since a known event ID.
Poll roughly every 30s, patch only changed items into the cache, and bump the
DAV `ctag` / `sync-token` so clients fetch just the delta.

## Known hard parts

1. **Login.** SRP plus 2FA plus a human-verification CAPTCHA. protoxide opens a
   browser popup for the challenge so credentials never pass through the app.
2. **Token refresh.** protoxide requires manual re-auth on expiry. Fixing this
   is the main reason carbonate exists — do it early, not last.
3. **Scheduling.** iTIP invitations travel over mail at Proton. protoxide does
   no server-side scheduling by design; decide deliberately whether we do.
4. **Recurrence exceptions and attendees.** The usual calendar edge cases, made
   worse by the encrypted split above.
5. **Cold start.** First full fetch and decrypt takes minutes for thousands of
   events. Cache aggressively.
6. **Signature verification.** protoxide leaves two upstream cryptographic
   checks disabled, so some old or imported events are served with an unverified
   signature. Understand why before copying that decision.
