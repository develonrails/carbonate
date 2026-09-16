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

## Serving CalDAV

go-webdav derives a resource's kind from **path depth**, not from anything the
backend says, so the URL layout is not a free choice:

Both handlers also strip a configured `Prefix` before measuring, which is what
lets CalDAV and CardDAV share one listener:

| Depth | CalDAV | CardDAV |
|---|---|---|
| — | prefix `/caldav` | prefix `/carddav` |
| 1 | `/caldav/principal/` | `/carddav/principal/` |
| 2 | `/caldav/principal/calendars/` | `/carddav/principal/contacts/` |
| 3 | `/caldav/principal/calendars/{token}/` | `/carddav/principal/contacts/default/` |
| 4 | `…/{uid}.ics` | `…/{uid}.vcf` |

Putting the principal at `/` looks tidier and quietly breaks discovery: the
root is its own resource type and serves only `current-user-principal`.

Proton has a single, unnamed collection of contacts, so the CardDAV address
book is a fixed one that carbonate neither creates nor deletes.

Two mappings are needed between Proton and URLs:

- **Calendar IDs** are base64 with `=` padding, which does not belong in a path,
  so a path segment is the first half of their SHA-256.
- **Resource names** are the client's to choose, and need not be the event UID.
  carbonate advertises UID-based names, but remembers what a client called
  something it PUT, or a later GET or DELETE at that path cannot find the event.

Reads are cached until Proton says the calendar has changed. The calendar
event loop's latest ID serves as both the CalDAV ctag and the cache's version:
one cheap request per poll decides whether to decrypt anything at all, so a
client polling every few seconds costs almost nothing while a change made
elsewhere appears on the next poll rather than after a timer.

The cache is also dropped on our own writes, because Proton records a change in
its event loop a moment after accepting it — straight after a write the token
still reads as it did before.

Contacts have no such token, so they are cached until carbonate writes.

The DAV backends take a `Store` interface rather than a Proton connection.
The methods are the operations DAV performs, not a window onto the API, which
keeps the fake in the tests small enough to be obviously right — and means the
backends can be tested without an account.

### Making a client see a writable calendar

GNOME Calendar connected on the first try but showed the calendar as
read-only. Two deviations in go-webdav 0.7.0 cause it, and `compat.go`
corrects both on the way out rather than forking the library:

- **`<privilege><read/><write/></privilege>`.** RFC 3744 defines
  `DAV:privilege` as holding a *single* privilege, so this is one malformed
  element rather than two. A client reading only the first child sees `read`
  and stops there. Each privilege now gets its own element.
- **`Allow` omits `PUT`.** go-webdav answers OPTIONS on a collection with
  `OPTIONS, PROPFIND, REPORT, DELETE, MKCOL`, which reads as a collection that
  cannot be written to. `PUT`, `GET` and `HEAD` are added.

- **No `getctag`.** The CalendarServer property that lets a client skip a sync
  entirely is absent, so `compat.go` supplies it: the name is removed from the
  PROPFIND before go-webdav sees it — asking for a property it does not know
  earns a 404 propstat that would then have to be unpicked — and the answer is
  added to the response afterwards.
- **An empty CardDAV filter matches nothing.** RFC 6352 makes the filter
  optional and an absent one matches everything, but go-webdav reads a filter
  with no conditions as matching nothing. A client that sends no filter would
  be shown an empty address book, so `QueryAddressObjects` returns everything
  in that case rather than calling the matcher.

- **`supported-report-set` answers 404**, and **`sync-collection` is not
  handled at all**. `compat.go` supplies the first and answers the second, so
  nothing is advertised that is not served.

### sync-collection

With a ctag a client knows *that* something changed; with sync-collection it
learns *what*, and fetches only that.

Proton's calendar event loop provides the deltas:
`GET /calendar/v1/{id}/modelevents/{cursor}` returns the events that moved,
a new cursor, `More` for paging and `Refresh` for "give up and read
everything".

Three things are not obvious:

- **The loop's `latest` ID is not a consumed cursor.** Handing it out as a
  sync token makes the next delta repeat whatever change produced it, so a
  client would refetch forever. A token is obtained by reading the loop *at*
  `latest` and taking the cursor that comes back.
- **The action codes are ignored.** Proton says what happened to each event,
  but carbonate looks the ID up in the current state instead: still there
  means changed, gone means removed. That needs no assumption about what the
  codes mean and cannot disagree with what a read would show.
- **A removed event is named by Proton ID alone**, while the client knows it
  by a path built from the iCalendar UID — which has gone with the event. The
  mapping is therefore recorded on every read. If a deletion arrives for
  something this process never saw, the client is told to read everything
  rather than left holding an event that no longer exists.

### The ctag

`GET /calendar/v1/{id}/modelevents/latest` returns the calendar event loop's
latest ID, which changes whenever anything in the calendar does. That is the
right source for a ctag: one cheap request. Deriving one from the events
instead would mean listing them, the very work the ctag exists to avoid.

It **lags a second or two** behind a write, because Proton records the change
in the event loop asynchronously. A client that writes and immediately polls
sees the old token — harmless, since it already has its own change, but it does
mean a second client learns of it a moment later.

## Packages

- `internal/proton` — Proton's private API: auth, key unlocking, event loop.
- `internal/session` — credential storage and refresh. Encrypted at rest with a
  locally generated bridge password. **Transparent token refresh lives here.**
- `internal/cache` — decrypted events and contacts on disk, keyed by a URL-safe
  path token so DAV requests resolve to Proton IDs in O(1). Files are 0600.
- `internal/calendar` — reading, decrypting, splitting and writing events.
- `internal/contacts` — the same for contacts, which need far less work: Proton
  stores them as vCards already and go-proton-api merges the cards on read.
- `internal/caldav` — go-webdav backend mapping CalDAV onto `internal/calendar`,
  plus `compat.go`, which corrects go-webdav's responses.
- `internal/carddav` — the same for CardDAV over `internal/contacts`.

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

- **Key salts need a scope a refreshed session loses.** The bridge kept
  stopping with `403 ... Access token does not have sufficient scope` on
  `/core/v4/keys/salts`, while every other request on the same session
  continued to work — `/core/v4/users` answered normally a moment earlier.
  Proton evidently treats the salts as sensitive and withdraws the right to
  read them from a session that has only been refreshed, never
  re-authenticated.

  So the salts are not read on resume at all: the salted passphrase is derived
  once at login and kept with the session. The fallback remains for a session
  stored before this, and for the day a key is rotated and the old passphrase
  stops opening anything.

  The first two explanations tried — too many sessions, then a session too old
  — were both wrong. What settled it was noticing which call in the sequence
  had failed: the error came from the salts, and the call before it had
  succeeded.
- **The mailbox password is not always the login password.** In two-password
  mode they are separate, and reusing the login password fails much later — at
  the local key unlock, with an error that says nothing about a second
  password existing. `mailboxPasswordFor` decides this from `Auth.PasswordMode`
  rather than assuming. The fake test server hardcodes one-password mode, so
  the branch is covered by unit tests rather than end to end.

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
  a new one, so `Resume` stores it before doing anything else that can fail,
  and again on every later refresh. Dropping a rotated token locks the user out
  until they log in again.

  `Resume` takes a `TokenStore` rather than a callback, and that is the whole
  point: a callback that does nothing compiles, runs, and leaves the stored
  session holding a dead token — a failure that surfaces much later as an
  account that appears broken. Three such callbacks were written during this
  project, one of them in code that shipped, before the shape was changed to
  one that cannot forget.
- **The app version is validated.** Proton rejects clients it does not
  recognise, so `CARBONATE_APP_VERSION` exists as an escape hatch.
- **The fake server serves TLS by default** with a self-signed certificate that
  go-proton-api refuses. Tests use `server.WithTLS(false)`.
- **Calendar names are not encrypted.** Almost everything in Proton Calendar
  is, so this surprises. The name lives in plaintext on the *member* record —
  `Calendars[].Members[].Name` — not on the calendar, because a shared calendar
  lets each participant name it for themselves. Pick the member whose email is
  one of yours.
- **Contacts have a bulk endpoint the library does not expose.**
  `/contacts/v4/contacts/export` returns the cards for a page of contacts; the
  listing carries the metadata but no cards, and go-proton-api's other call
  fetches one contact at a time. Reading an address book is two requests, not
  one per contact.
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

**Verified:** create, update and delete all round-trip against a live account
and show up correctly in the Proton web app. On update, a client sending
`SEQUENCE:0` had it rewritten to `SEQUENCE:1`; without that the edit would have
been discarded in silence.

Key packets are **reused on update**, not regenerated: re-keying an event on
every edit would churn the copy each attendee holds. A new key packet is only
sent when there is none to reuse.

One detail the read side makes obvious: **the signature covers the plaintext,
not the ciphertext.** Sign first, then encrypt, and send the ciphertext beside
a signature over what went into it.

### One endpoint for everything

`PUT /calendar/v1/{calendarID}/events/sync` takes a batch of entries and
handles create, update and delete through different entry shapes. There is no
separate POST or DELETE. Responses are per-entry, so a batch can partially
fail and the per-entry code is where the real reason lives.

**The two response shapes differ.** A create or update answers with one entry
in `Responses` carrying the stored event. A delete answers
`{"Code":1001,"Responses":[]}` — nothing per entry at all. Code **1001** is not
an error: it is what a batch endpoint reports at the top level, meaning the
real verdicts follow per entry. Treating it as a failure makes a successful
delete look broken, which is exactly what happened here.

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
| Attendees | encrypted + signed | `uid` `dtstamp` `attendee` |

### Recurring events and their exceptions

Proton stores an exception — one occurrence moved or edited — as a **separate
event with the same UID**, told apart by `RecurrenceID`. A client sends both in
one object (RFC 4791 §4.1), so both directions need converting.

The mistake that made this look impossible for a while: matching a write by UID
alone found the series, turned the exception into an update of it, and sent
`RECURRENCE-ID` on an event that also has an `RRULE`. Proton refuses that with
code 2011, "These properties are not supported" — a message about properties
for a problem about identity. Writes match on UID *and* recurrence.

On the way out, the components are grouped back into one resource. Serving them
separately would put two resources at one address, since the path is built from
the UID. The ETag covers every component, so editing one occurrence changes the
tag of the whole resource, and deleting the resource takes the exceptions with
it — an exception left behind is an occurrence of a series that no longer
exists.

`uid` and `dtstamp` repeat in every card — which is exactly what the live event
showed on the read side, and why reassembly has to collapse them.

Two rules that are not obvious from the table:

- **Unrecognised properties go into the shared encrypted card.** Encrypting
  what you do not understand is the safe default.
- **A card holding only `uid` and `dtstamp` is dropped entirely** rather than
  sent as an empty encrypted blob. This is why our live test event came back
  with no `AttendeesEvents` and a null `CalendarKeyPacket`.

### Reminders

VALARM does not go into an ICS part at all. Alarms travel as a separate
`Notifications` field on the event, beside the encrypted cards, as
`{Type, Trigger}` — where `Type` is 0 for email and 1 for the device, and
`Trigger` is the iCalendar value verbatim, such as `-PT15M`. Proton knows only
those two kinds, so an AUDIO alarm becomes a device notification.

`Notifications` is **always sent, never omitted**. Leaving it out of an update
keeps whatever was there before, so removing the last reminder would appear to
work and change nothing.

Reading them back needs a field go-proton-api does not decode, so
`internal/calendar` fetches events itself and embeds go-proton-api's struct
rather than replacing it: the embedded fields still handle the parts and the
crypto, and `encoding/json` fills both from one response.

### Attendees

protoxide leaves this unimplemented, so the shape below is read from Proton's
own web client (`packages/shared/lib/calendar/formatData.ts` and
`attendees.ts`).

An attendee appears in two places at once:

- **`AttendeesEventContent`** — the `ATTENDEE` properties, encrypted under the
  *shared* session key so that everyone invited can read them, and signed.
- **`Attendees`** — a clear list of `{Token, Status}`. Proton tracks replies
  without ever learning who was invited.

The token is `SHA1(uid + normalisedEmail)`, hex-encoded, and is written back
into the event as an `X-PM-TOKEN` parameter. Both ends derive it from the same
inputs, so it has to match exactly: strip `mailto:` and lowercase. SHA-1 is
Proton's choice of identifier, not a security decision.

Status maps onto the same integers as iCalendar's PARTSTAT:

| `PARTSTAT` | Proton |
|---|---|
| `NEEDS-ACTION` | 0 |
| `TENTATIVE` | 1 |
| `DECLINED` | 2 |
| `ACCEPTED` | 3 |

Two more fields travel beside them, and unlike the pair above they are keyed
by address rather than by token — Proton has to know *who*, not merely that
somebody changed:

- **`AddedProtonAttendees`** — `{Email, AddressKeyPacket}`, the shared session
  key wrapped to a guest's own address key. Being on the list is not enough to
  read the event: the parts a guest is meant to see are encrypted under that
  session key, and this is the only thing that hands them a copy. Sent only for
  guests newly added, since re-wrapping the key for someone who already holds
  it buys nothing. Whether a guest is new is read from the tokens Proton keeps
  in the clear, so it costs no decryption to know.
- **`RemovedAttendeeAddresses`** — the addresses dropped since the stored
  version. This one *does* cost a decryption: the clear list holds only tokens,
  which are hashes, so the stored guest list has to be read back to find out
  who is leaving. Failing that is not worth refusing a write over, so it
  degrades to telling nobody.

A guest outside Proton gets no key packet — there is no key of theirs to wrap
one to. An address Proton will not answer for is reported on stderr and
skipped, leaving the guest recorded but unable to open the event. Nor does the
organiser get one: clients routinely list them among the attendees, and they
already open the event through the calendar key.

The one case where every guest is re-keyed is an event that had no stored
shared key packet to reuse, so the write minted a fresh one. The packets guests
already hold then open nothing, and handing them only to the newly invited
would quietly lock out everyone who was already there.

Still not implemented: the invitation itself. Those travel by mail at Proton,
as iTIP messages, and carbonate sends no mail — so a guest is recorded, handed
a key, and never told.

### Three ways Proton rejects a write

Each of these is a comment in protoxide's source, meaning someone lost an
afternoon to it:

1. **`SEQUENCE` must strictly increase** on an update, or Proton *silently*
   drops the edit. Clients do not reliably bump it, so the bridge has to force
   `old + 1`. The stored value is readable without the calendar key, since
   Proton keeps `SEQUENCE` in the signed — and therefore cleartext — shared
   card.
2. **`SEQUENCE` must be emitted bare** — `SEQUENCE:0`. Encoding it as
   `SEQUENCE;VALUE=TEXT:0`, which go-ical's `SetText` does, earns a 500.
3. **Sign with the member's own address key.** Defaulting to the first key in
   the keyring fails on accounts with several addresses, with code **2001**,
   "Provide data signed using the address key".
4. **Attendees require an organiser.** An event with `ATTENDEE` and no
   `ORGANIZER` is refused with "Shared event has attendees but does not have an
   organizer". Clients do not always send one, so carbonate fills in the
   calendar member's own address — we are writing to our own calendar, so we
   are the organiser.

## Known hard parts

1. **Login.** SRP plus 2FA plus a human-verification CAPTCHA. protoxide opens a
   browser popup for the challenge so credentials never pass through the app.
2. **Token refresh.** protoxide requires manual re-auth on expiry. Fixing this
   is the main reason carbonate exists — do it early, not last.
3. **Scheduling.** iTIP invitations travel over mail at Proton. protoxide does
   no server-side scheduling by design; decide deliberately whether we do.
   carbonate now shares the session key with Proton guests, which is the half
   of the problem that needs no SMTP path — the mail half still does.
4. **Recurrence exceptions and attendees.** The usual calendar edge cases, made
   worse by the encrypted split above.
5. **Cold start.** First full fetch and decrypt takes minutes for thousands of
   events. Cache aggressively.
6. **Signature verification.** protoxide leaves two upstream cryptographic
   checks disabled, so some old or imported events are served with an unverified
   signature. Understand why before copying that decision.
