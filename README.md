# carbonate

A third-party CalDAV and CardDAV bridge for Proton Calendar and Proton Contacts,
so standards-compliant clients — Thunderbird, Evolution, Calendar.app, DAVx5 —
can talk to Proton.

> **Status: calendars and contacts both work.** `carbonate serve` exposes
> Proton Calendar over CalDAV and Proton Contacts over CardDAV, reads and
> writes verified against live Proton, attendees included. See
> [Roadmap](#roadmap) for what is still missing.

## Why

Proton has no CalDAV or CardDAV support and no public API. That is a deliberate
consequence of end-to-end encryption: DAV is a server-side protocol that expects
the server to read and edit your data, and Proton's servers cannot. The bridge
model — the one Proton Mail Bridge already uses for IMAP and SMTP — is the way
out: decrypt locally, then re-expose the plaintext over a standard protocol on
localhost.

Two projects got there first, and carbonate stands on both:

- [hydroxide](https://github.com/emersion/hydroxide) — CardDAV, IMAP and SMTP.
  Casually maintained; archived on GitHub in August 2026 and moved to Codeberg.
- [protoxide](https://github.com/mathewcsims/protoxide) — CalDAV, two-way sync.

Neither does calendar *and* contacts in one daemon, and neither has reliable
silent session refresh — today you re-authenticate by hand when your token
expires. Those two gaps are carbonate's reason to exist.

## Design

```
CalDAV/CardDAV client  →  go-webdav backend  →  local cache (decrypted)
                                                      ↑
                                         event-loop poller (~30s delta)
                                                      ↓
                                        go-proton-api  →  Proton API
```

Proton's data model happens to be the DAV formats already:

- **Contacts** are vCards, split into cards by protection level (cleartext,
  signed, encrypted+signed) under your address key.
- **Events** are iCalendar, split across `SharedEvents`, `CalendarEvents`,
  `AttendeesEvents` and `PersonalEvents`, each with signed and encrypted
  sections under a per-calendar key.

Reading means reassembling those parts; writing means splitting them back apart
correctly. carbonate now does both. go-proton-api offers no calendar write
endpoints, so carbonate calls Proton's sync endpoint itself — the property-split
table comes from protoxide, and the ways Proton rejects a bad write are
documented in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detail.

## Install

Requires Go 1.26 or newer.

```sh
make build      # or: go build ./cmd/carbonate
```

## Usage

```sh
carbonate auth <username>   # log in to Proton, store an encrypted session
carbonate calendars         # list calendars; --events to show them, --ics for raw iCalendar
carbonate event put         # create or replace an event from iCalendar on stdin
carbonate event delete      # delete an event by its iCalendar UID
carbonate contacts          # list contacts; --vcard for the full vCard
carbonate serve             # serve CalDAV and CardDAV on 127.0.0.1:8080
```

```console
$ carbonate calendars --events
Test kalender! — 1 event(s)
  2026-09-15 (all day)  Test!

$ carbonate event put < event.ics
Created event kH1f5bFeN3p7LB0D22GANXAbVo9uyQQf...

$ carbonate event put < edited.ics      # same UID
Updated event kH1f5bFeN3p7LB0D22GANXAbVo9uyQQf...

$ carbonate event delete --uid meeting@example.com
Deleted event meeting@example.com
```

`event put` is CalDAV PUT semantics: the client owns the UID and does not need
to know whether Proton has seen it before. The write path is therefore
exercised exactly as the DAV endpoint will use it.

`auth` prints a randomly generated **bridge password** once. It encrypts the
session file, and your DAV clients will use it as their password.

### Connecting a client

With `carbonate serve` running, sign in as your Proton address with the bridge
password:

| | |
|---|---|
| Calendar | `http://127.0.0.1:8080/caldav/` |
| Contacts | `http://127.0.0.1:8080/carddav/` |

In GNOME Calendar: **Calendars → Add calendar → Add from web**. Evolution,
Thunderbird and DAVx5 take the same three values. `/.well-known/caldav` and
`/.well-known/carddav` both redirect to the right principal.

HTTP is deliberately unencrypted: the listener binds to loopback only, and
basic auth is there to stop other local processes reaching your calendar
rather than to protect the wire.

Set `CARBONATE_BRIDGE_PASSWORD` to run unattended, and `CARBONATE_DEBUG=1` to
dump every request and response when the API misbehaves — it prints access
tokens, so leave it off otherwise.

## Testing

```sh
make test       # unit and integration tests
make race       # the run that matters for concurrent token refresh
make cover      # coverage summary
```

Auth is covered by integration tests against the fake Proton server that ships
with go-proton-api, so login, key unlocking, token rotation and session
revocation are all exercised without a real account or network access.

## Roadmap

- [x] SRP login and TOTP two-factor
- [x] Two-password mode (separate mailbox password)
- [x] Encrypted session at rest, rotated refresh tokens persisted
- [x] Anonymous session handshake (Proton rejects logins without one)
- [x] Unattended login via `--password-stdin`
- [ ] Human-verification (CAPTCHA) challenge
- [x] Calendar: fetch, decrypt and reassemble events into iCalendar
- [x] Calendar: split, encrypt, sign and create events
- [x] Calendar: update and delete events, with SEQUENCE handled correctly
- [x] Serve calendars over CalDAV, read and write, from GNOME Calendar
- [ ] Calendar: recurrence exceptions and attendees
- [ ] `getctag` and sync-token, so clients need not re-read everything
- [ ] Event-loop driven cache instead of a short TTL
- [x] Contacts: decrypt, split, and serve over CardDAV
- [x] Attendees: tokens, encrypted attendee part, RSVP status
- [ ] Invitations by mail, so an attendee is told they were invited
- [ ] Event-loop poller and delta sync, mapped to DAV ctag / sync-token
- [ ] Write path: create, edit, delete round-tripping to Proton
- [ ] Recurrence exceptions and attendees

## Scope

Calendar and contacts only.

**Mail** is already handled by the official [Proton Mail
Bridge](https://proton.me/mail/bridge), which is supported and does the job.

**Files** are out of scope. Proton shipped an official [Drive
CLI](https://proton.me/blog/proton-drive-cli) for Linux in June 2026, and
[rclone](https://rclone.org/protondrive/) has had a Proton Drive backend for a
while. File sync also wants WebDAV or a FUSE mount, not CalDAV — a different
protocol and a different problem.

## Security and risk

- This talks to **Proton's private, internal API**. There is no stability
  guarantee and no Proton QA. It can break at any time.
- Using an unofficial client is your own call with respect to Proton's terms.
  Use your own account.
- The session file is a long-lived credential, and the cache holds **decrypted**
  events and contacts on disk. Both are written `0600` — the same trade-off any
  desktop mail client makes, but make it knowingly.
- Never commit `auth.json` or `calcache-*.json`. They are in `.gitignore`.

## License

MIT — see [LICENSE](LICENSE).

carbonate ports and adapts code from hydroxide and protoxide, both MIT. Their
copyright notices are retained in the files derived from them.

## Name

Carbonate (CO₃²⁻) is diprotic: it accepts two protons. This daemon speaks two
protocols to one Proton. It also keeps the `-oxide` family tradition started by
hydroxide, where OH⁻ + H⁺ gives you water.
