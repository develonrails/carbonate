# carbonate

A third-party CalDAV and CardDAV bridge for Proton Calendar and Proton Contacts,
so standards-compliant clients — Thunderbird, Evolution, Calendar.app, DAVx5 —
can talk to Proton.

> [!WARNING]
> **carbonate is a work in progress, and not yet released.** There is no
> tagged version and nothing on Flathub yet, so there is no upgrade path
> between builds. It talks to a private API that Proton can change without
> warning, and it holds the keys to your calendar and contacts. Treat it as
> something to experiment with, keep a way back to the web app, and expect to
> re-read this file after pulling.

> **Status: calendars and contacts both work**, in both directions, verified
> against a live Proton account — attendees, reminders, recurring events and
> invitations included. See [Status](#status) for what is still missing.

## Documentation

| | |
|---|---|
| [docs/SETUP.md](docs/SETUP.md) | Connecting a client — GNOME Online Accounts, GNOME Calendar, GNOME Contacts, everything else |
| [docs/OPERATING.md](docs/OPERATING.md) | Running it day to day — the bridge password, sessions, logs, what to do when nothing syncs |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | How it works inside, and what Proton's private API actually does |
| [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) | Building, testing, packaging |

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
  signed, encrypted+signed).
- **Events** are iCalendar, split across `SharedEvents`, `CalendarEvents`,
  `AttendeesEvents` and `PersonalEvents`, each with signed and encrypted
  sections under a per-calendar key.

Reading means reassembling those parts; writing means splitting them back apart
correctly. go-proton-api offers no calendar write endpoints, so carbonate calls
Proton's sync endpoint itself. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) has
the detail, including the ways Proton rejects a bad write.

## Install

The Flatpak carries the window, the command-line tool and a recent GTK, so
there is nothing to build and no GLib version to worry about.

**carbonate is not on Flathub yet.** Until it is, take the bundle CI builds
from the current `main` — [the `continuous`
release](https://github.com/develonrails/carbonate/releases/tag/continuous):

```sh
flatpak install --user --or-update carbonate-x86_64.flatpak
```

That bundle is replaced by every green CI run, so it moves with `main` and
carries no upgrade guarantees.

`--or-update` is what makes the second one work. Every bundle is the same ref
at the same version — `master`, 0.1.0 — because nothing bumps a version between
CI runs, so plain `install` sees something already installed and stops. Nor can
`flatpak update` help: a bundle is a file, not a remote, so there is nothing for
it to pull from. The flag says "replace whatever is there", which for a rolling
build is what was meant all along. Once there is a Flathub listing this becomes:

```sh
flatpak install flathub io.github.develonrails.Carbonate   # not yet available
```

To build it yourself, see [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md).

## First run

Open Carbonate, sign in to Proton once, and press Start. The window shows the
address and the bridge password to give your calendar app. Turning on **Start
at login** asks your system for permission to run in the background, so the
bridge is already there the next time your apps look for it — you can withdraw
that later in your system's background app settings.

Signing in is the one step that cannot be automated: Proton asks for your
password, possibly a second factor, and occasionally a human-verification
challenge. Everything after it runs unattended.

The same thing from a terminal:

```sh
carbonate auth <username>   # log in to Proton, store an encrypted session
carbonate serve             # serve CalDAV and CardDAV on 127.0.0.1:8080
```

`auth` prints a randomly generated **bridge password** once. It encrypts the
session file, and your DAV clients will use it as their password.

Then point a client at:

| | |
|---|---|
| Calendar | `http://127.0.0.1:8080/caldav/` |
| Contacts | `http://127.0.0.1:8080/carddav/` |

On GNOME, **Settings → Online Accounts → Calendar, Contacts and Files** sets up
both at once. [docs/SETUP.md](docs/SETUP.md) covers that and every other client,
including the details that are easy to get wrong.

## Commands

```sh
carbonate auth <username>   # log in to Proton, store an encrypted session
carbonate calendars         # list calendars; --events to show them, --ics for raw iCalendar
carbonate event put         # create or replace an event from iCalendar on stdin
carbonate event delete      # delete an event by its iCalendar UID
carbonate contacts          # list contacts; --vcard for the full vCard
carbonate sessions          # list Proton sessions; -revoke-stale to clear old ones
carbonate serve             # serve CalDAV and CardDAV on 127.0.0.1:8080
carbonate serve -log        # ...and report every request and every change
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

There is also `carbonate-gui`, a small GTK window for logging in and starting
the server without keeping a terminal open. It runs the same server from the
same package, so the two cannot drift apart.

## Status

Done:

- [x] SRP login, TOTP, two-password mode, anonymous session handshake
- [x] Encrypted session at rest, with rotated refresh tokens persisted
- [x] Unattended login via `--password-stdin`
- [x] Calendar: decrypt, reassemble, create, update, delete
- [x] Attendees: tokens, encrypted attendee part, RSVP status
- [x] Invitations mailed to guests over Proton's own iTIP path
- [x] Reminders, in both directions
- [x] Contacts: decrypt, split, create, update, delete
- [x] Serve both over CalDAV and CardDAV, with `getctag` and `sync-collection`

Not done, and worth knowing before relying on carbonate:

- No tagged release, no Flathub listing, no upgrade path between builds.
- Cold start on a large account is slow: the first fetch decrypts everything.
- One Proton account per session file.

Open issues live in
[the tracker](https://github.com/develonrails/carbonate/issues), each with its
reasoning.

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
- HTTP is deliberately unencrypted: the listener binds to loopback only, and
  basic auth is there to stop other local processes reaching your calendar
  rather than to protect the wire.
- Never commit `auth.json` or `calcache-*.json`. They are in `.gitignore`.

## License

MIT — see [LICENSE](LICENSE).

carbonate ports and adapts code from hydroxide and protoxide, both MIT. Their
copyright notices are retained in the files derived from them.

## Name

Carbonate (CO₃²⁻) is diprotic: it accepts two protons. This daemon speaks two
protocols to one Proton. It also keeps the `-oxide` family tradition started by
hydroxide, where OH⁻ + H⁺ gives you water.
