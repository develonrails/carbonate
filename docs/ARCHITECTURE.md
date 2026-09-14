# Architecture

## Dependencies

| Concern | Library | Status |
|---|---|---|
| Proton API client, event loop | `github.com/ProtonMail/go-proton-api` | in use (master pseudo-version) |
| SRP authentication | `github.com/ProtonMail/go-srp` | via go-proton-api |
| OpenPGP | `github.com/ProtonMail/gopenpgp/v2` | via go-proton-api |
| CalDAV/CardDAV server | `github.com/emersion/go-webdav` | not yet added |
| iCalendar / vCard parsing | `github.com/emersion/go-ical`, `go-vcard` | not yet added |

### Pinning go-proton-api

**Do not run `go get github.com/ProtonMail/go-proton-api@latest`.** The newest
tag is v0.4.0, from 2022, but development continued on `master` without tagging.
Go's version ordering treats the current `v0.0.0-2026...` pseudo-version as
*older* than v0.4.0, so `@latest` silently downgrades you by four years. Use
`@master`.

v0.4.0 cannot log in at all any more — see the anonymous session below.

go-proton-api also depends on **Proton's fork of resty** via a `replace`
directive. Go ignores `replace` in dependencies, so our `go.mod` repeats it;
without it the build fails on missing multipart-stream APIs.

### What go-proton-api actually gives us

Worth knowing before planning the write path:

- **Calendar is read-only.** `GetCalendars`, `GetCalendarKeys`,
  `GetCalendarPassphrase`, `GetCalendarEvents` and friends exist, but there is
  no create, update or delete. The CalDAV write path will need endpoints we
  call ourselves.
- **Its structs have drifted from the live API.** `Calendar.Name` is the
  clearest case: the field no longer exists at the top level, so `GetCalendars`
  returns every calendar unnamed. `Conn.Get` exists for exactly this — raw
  authenticated requests decoded into our own structs. Expect to need it more
  as the API moves on.
- **Contacts have full CRUD** — `CreateContacts`, `UpdateContact`,
  `DeleteContacts`. CardDAV can be complete on this library alone.
- **Drive is partly there** (`ListVolumes`, `ListShares`, `GetLink`,
  `ListChildren`, `GetBlock`) — read-only, and out of scope for carbonate.
- **A fake Proton server ships in `go-proton-api/server`.** It backs our auth
  integration tests: real SRP, real key unlocking, no network and no account.
  It does *not* implement the calendar endpoints, so calendar work will need
  its own test doubles.

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

## Verified against a live account

The read path is not theoretical. Against a real Proton account with one
all-day event, carbonate decrypts and reassembles:

```
Test kalender! — 1 event(s)
  2026-09-15 (all day)  Test!
```

The event arrives in three encrypted pieces, each its own complete VCALENDAR:

| Part | Type | Contents |
|---|---|---|
| `SharedEvents[0]` | signed, cleartext | `UID`, `DTSTAMP`, `DTSTART;VALUE=DATE`, `SEQUENCE` |
| `SharedEvents[1]` | encrypted + signed | `SUMMARY` |
| `CalendarEvents[0]` | signed, cleartext | `STATUS` |

Reassembly merges the VEVENT bodies and drops the duplicated wrappers — `UID`
and `DTSTAMP` repeat in every part and must appear once, while `ATTENDEE` and
`EXDATE` legitimately repeat and must not be deduplicated.

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

## Gotchas found the hard way

- **Logging in needs an anonymous session first.** Proton now answers
  `/auth/v4/info` with `401 Invalid access token` unless the request already
  belongs to a session — even though logging in is by definition
  unauthenticated. `POST /auth/v4/sessions` returns one; its UID and access
  token go on every manager-level request. go-proton-api does not do this
  itself, so `internal/proton/unauth.go` does. This is why v0.4.0 is unusable
  against production, and it is invisible until you try a real account: the
  fake test server predates the change.
- **`Keys.Unlock` used to report success on a wrong password**, skipping keys
  it could not open and returning a nil error. Current master returns "not able
  to unlock any key" instead. `unlock` handles both, and still checks
  `CountDecryptionEntities()`.
- **Refresh tokens rotate.** Proton discards the old token the moment it issues
  a new one, so `Resume` persists before doing anything else that can fail, and
  registers an auth handler for later refreshes. Dropping a rotated token locks
  the user out until they log in again.
- **The app version is validated.** Proton rejects clients it does not
  recognise, so `CARBONATE_APP_VERSION` exists as an escape hatch.
- **The fake server serves TLS by default** with a self-signed certificate that
  go-proton-api refuses. Tests use `server.WithTLS(false)`.
- **Calendar names are not encrypted.** Almost everything in Proton Calendar
  is, so this surprises. The name lives in plaintext on the *member* record —
  `Calendars[].Members[].Name` — not on the calendar, because a shared calendar
  lets each participant name it for themselves. Pick the member whose email is
  one of yours.
- **`GetAllCalendarEvents` cannot be used.** It pages at go-proton-api's
  library-wide `maxPageSize` of 150, which the calendar endpoint rejects with
  "Invalid page size parameter" (code 2021). `internal/calendar` pages by hand
  at 100.
- **A full-day event starts at midnight UTC.** Rendering that in the local zone
  shows a spurious time, and the wrong date west of Greenwich. Check `FullDay`
  before formatting.
- **`CARBONATE_DEBUG=1`** dumps full requests and responses. It is how the two
  findings above were diagnosed, and it prints access tokens, so keep it off
  by default.

## The write path

carbonate creates events by calling Proton's sync endpoint directly, since
go-proton-api has none. The property-split table below is read from
protoxide, which does not use go-proton-api at all — it carries its own hand-written `protonmail` client
inherited from hydroxide, so the missing endpoints were simply written. What
follows is read from [protoxide](https://github.com/mathewcsims/protoxide)
(MIT). carbonate reimplements the split against gopenpgp rather than copying
code, but the table itself is protoxide's work.

**Verified:** an event created by `carbonate event add` — with DTSTART, DTEND,
SUMMARY, LOCATION, DESCRIPTION and STATUS — round-trips through Proton and
reads back with every property intact and its signatures verifying.

One detail the read side makes obvious: **the signature covers the plaintext,
not the ciphertext.** Sign first, then encrypt, and send the ciphertext beside
a signature over what went into it.

### One endpoint for everything

`PUT /calendar/v1/{calendarID}/events/sync` takes a batch of entries and
handles create, update and delete through different entry shapes. There is no
separate POST or DELETE. Responses are per-entry, so a batch can partially
fail and the per-entry code is where the real reason lives.

Create versus update is decided by first fetching the event: API code **2061**
("not a valid ID") means it does not exist yet.

`GET /calendar/v1/{id}/bootstrap` returns keys, members and passphrase in a
single call — cheaper than the three separate requests carbonate currently
makes.

### Which property goes in which part

This is the table I expected to have to derive by experiment:

| Part | Card | Properties |
|---|---|---|
| Shared | signed | `uid` `dtstamp` `dtstart` `dtend` `recurrence-id` `rrule` `exdate` `organizer` `sequence` |
| Shared | encrypted + signed | `uid` `dtstamp` `created` `description` `summary` `location` |
| Calendar | signed | `uid` `dtstamp` `exdate` `status` `transp` |
| Calendar | encrypted + signed | `uid` `dtstamp` `comment` |

`uid` and `dtstamp` repeat in every card — which is exactly what the live event
showed on the read side, and why reassembly has to collapse them.

Two rules that are not obvious from the table:

- **Unrecognised properties go into the shared encrypted card.** Encrypting
  what you do not understand is the safe default.
- **A card holding only `uid` and `dtstamp` is dropped entirely** rather than
  sent as an empty encrypted blob. This is why our live test event came back
  with no `AttendeesEvents` and a null `CalendarKeyPacket`.

VALARM does not go into an ICS part at all; alarms travel as a separate
`Notifications` JSON field on the event.

### Three ways Proton rejects a write

Each of these is a comment in protoxide's source, meaning someone lost an
afternoon to it:

1. **`SEQUENCE` must strictly increase** on an update, or Proton *silently*
   drops the edit. Clients do not reliably bump it, so the bridge has to force
   `old + 1`.
2. **`SEQUENCE` must be emitted bare** — `SEQUENCE:0`. Encoding it as
   `SEQUENCE;VALUE=TEXT:0`, which go-ical's `SetText` does, earns a 500.
3. **Sign with the member's own address key.** Defaulting to the first key in
   the keyring fails on accounts with several addresses, with code **2001**,
   "Provide data signed using the address key".

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
