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

Neither does calendar *and* contacts in one daemon, and neither refreshes its
session silently — with both, you re-authenticate by hand when the token
expires. Those two gaps are carbonate's reason to exist.

## Design

```
CalDAV/CardDAV client  →  go-webdav backend  →  local cache (decrypted)
                                                      ↑
                                     refetch only when Proton's
                                       change token has moved
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

There is also a small GTK window, `carbonate-gui`, for logging in and starting
the server without keeping a terminal open. It runs the same server from the
same package, so the two cannot drift apart. See
[Building the GUI](#building-the-gui).

```sh
carbonate auth <username>   # log in to Proton, store an encrypted session
carbonate calendars         # list calendars; --events to show them, --ics for raw iCalendar
carbonate event put         # create or replace an event from iCalendar on stdin
carbonate event delete      # delete an event by its iCalendar UID
carbonate contacts          # list contacts; --vcard for the full vCard
carbonate sessions          # list Proton sessions; -revoke-stale to clear old ones
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

GNOME Contacts has no such dialog — see [docs/CLIENTS.md](docs/CLIENTS.md).

Logging in again mints a new bridge password, which every client configured
with the old one would then reject. `carbonate auth -keep-bridge-password`
keeps the current one.

HTTP is deliberately unencrypted: the listener binds to loopback only, and
basic auth is there to stop other local processes reaching your calendar
rather than to protect the wire.

Set `CARBONATE_BRIDGE_PASSWORD` to run unattended, and `CARBONATE_DEBUG=1` to
dump every request and response when the API misbehaves — it prints access
tokens, so leave it off otherwise.

## Building the GUI

The window is behind the `gtk` build tag, so the daemon, the tests and CI need
no C toolchain at all.

```sh
make gui
```

It needs `gtk4` and `gobject-introspection` development headers, and a **recent
GLib** — gotk4 calls functions that Debian and Ubuntu packages do not yet
provide, so the build fails there on missing symbols. Fedora 44 is new enough.

On an immutable host such as Fedora Silverblue there are no headers and no C
compiler either; build it in a container:

```sh
toolbox create carbonate-build
toolbox run -c carbonate-build sudo dnf install -y gcc glibc-devel gtk4-devel gobject-introspection-devel golang
toolbox run -c carbonate-build make gui
```

The result links against the host's own GTK, so it runs outside the container.

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

Done:

- [x] SRP login, TOTP, two-password mode, anonymous session handshake
- [x] Encrypted session at rest, with rotated refresh tokens persisted
- [x] Unattended login via `--password-stdin`
- [x] Calendar: decrypt, reassemble, create, update, delete
- [x] Attendees: tokens, encrypted attendee part, RSVP status
- [x] Reminders, in both directions
- [x] Contacts: decrypt, split, create, update, delete
- [x] Serve both over CalDAV and CardDAV, with `getctag`

Open, each with its reasoning in the issue:

| | |
|---|---|
| [#1](https://github.com/develonrails/carbonate/issues/1) | Recurrence exceptions are probably mishandled |
| [#2](https://github.com/develonrails/carbonate/issues/2) | Invitations are never sent to attendees |
| [#4](https://github.com/develonrails/carbonate/issues/4) | Human verification (CAPTCHA) at login is not handled |
| [#7](https://github.com/develonrails/carbonate/issues/7) | `sync-collection` REPORT is not supported |

One is worth knowing before you rely on carbonate: an attendee is recorded but
never told they were invited ([#2](https://github.com/develonrails/carbonate/issues/2)).

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
