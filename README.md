# carbonate

A third-party CalDAV and CardDAV bridge for Proton Calendar and Proton Contacts,
so standards-compliant clients — Thunderbird, Evolution, Calendar.app, DAVx5 —
can talk to Proton.

> **Status: scaffold.** Nothing works yet. The commands parse their flags and
> tell you they are not implemented. See [Roadmap](#roadmap).

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

Reading means reassembling those parts. Writing means splitting them back apart
correctly — which is where the real work is.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detail.

## Install

Requires Go 1.22 or newer.

```sh
go build ./cmd/carbonate
```

## Usage

```sh
carbonate auth <username>   # log in to Proton, store an encrypted session
carbonate serve             # serve CalDAV and CardDAV on 127.0.0.1:8080
```

Point your client at `http://127.0.0.1:8080` with your Proton username and the
bridge password printed by `auth`. HTTP is deliberately unencrypted: the
listener binds to loopback only.

## Roadmap

- [ ] SRP login, 2FA, human-verification challenge
- [ ] Encrypted session at rest, **transparent token refresh**
- [ ] Contacts: decrypt, reassemble, serve over CardDAV (read)
- [ ] Calendar: decrypt, reassemble, serve over CalDAV (read)
- [ ] Event-loop poller and delta sync, mapped to DAV ctag / sync-token
- [ ] Write path: create, edit, delete round-tripping to Proton
- [ ] Recurrence exceptions and attendees

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
