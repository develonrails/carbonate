# Connecting clients

`carbonate serve` listens on `127.0.0.1:8080`. Sign in as your Proton address
with the bridge password printed by `carbonate auth`.

| | |
|---|---|
| Calendar | `http://127.0.0.1:8080/caldav/` |
| Contacts | `http://127.0.0.1:8080/carddav/` |

## Keep the bridge password

`carbonate auth` generates a new bridge password every time, and every client
configured with the old one stops working. After the first login, use:

```sh
carbonate auth <username> -keep-bridge-password
```

## One process at a time

Proton rotates the refresh token on every use and discards the old one, so two
carbonate processes sharing a session would each invalidate the other. A
session is therefore locked while it is in use, and a second process is turned
away:

```
carbonate is already running and using this session; stop it first
```

Stop the server, or point the other command at a different session file with
`-session`.

If a command ever reports that the session no longer has full access, log in
again with `-keep-bridge-password`: your clients keep working, since the bridge
password does not change.

`carbonate sessions` shows what is open on the Proton side. Proton allows a
limited number and takes access away from older ones rather than refusing new
ones, so requests eventually fail for reasons that look unrelated;
`-revoke-stale` ends carbonate's own earlier sessions and leaves other clients
alone.

## If Proton asks for human verification

Proton sometimes wants proof that a person is present. carbonate prints a link,
you open it, solve the challenge, and paste back the token it gives you:

```
Proton wants human verification (captcha). Open this, solve it, and paste the
token it gives you:
  https://verify.proton.me/?embed=false&methods=captcha&token=...

Verification token:
```

The link is printed rather than opened, since carbonate often runs without a
desktop. Unattended login reads the token as the next line of standard input,
which only helps if you already had a browser.

The window has a **Verification token** field; leave it empty until the link
appears below the form, then fill it in and log in again.

## Two-password accounts

Proton accounts can keep two passwords apart:

| | |
|---|---|
| Login password | proves to Proton that you are you |
| Mailbox password | decrypts your data, and never leaves your machine |

Most accounts use one password for both, and you will never notice. If yours
separates them, Proton's server holds nothing that can open your data: someone
with your login password could sign in and still read nothing.

carbonate asks for the second password only when the account actually uses one:

```sh
carbonate auth you@proton.me            # prompts for the mailbox password
printf 'login\nmailbox\n' | carbonate auth you@proton.me --password-stdin
```

Unattended login reads the answers as successive lines, in the order they are
asked. The window has a field for it; leaving it empty falls back to the login
password.

## Watching what the bridge is doing

`carbonate serve -log` reports every DAV request and every change Proton
sends. Without it the bridge is silent: a request that failed is visible only
to the client that received the failure, and a client reports that as a
calendar that will not update, or as nothing at all.

```console
$ carbonate serve -log
15:04:38 PROPFIND  /caldav/principal/calendars/12081a3d…/ 207 6ms
15:04:38 REPORT    /caldav/principal/calendars/12081a3d…/ 207 8ms
carbonate: calendar 7WcS1DSk…: Proton reports a change (LU4nT24F…)
carbonate: calendar 7WcS1DSk…: read 14 events from Proton
carbonate: calendar 7WcS1DSk…: created meeting@example.com
```

Lines with a time and a status are your client; lines beginning `carbonate:`
are Proton. An idle bridge only shows the former, because a poll that found
nothing is not worth a line.

That split is what makes the common reports separable:

| | |
|---|---|
| No lines at all | the client is not polling — see below |
| Requests, but never `Proton reports a change` | the change is not reaching carbonate |
| `reports a change`, client still empty | the client is not acting on it |
| A 4xx or 5xx with a reason | that reason is the bug |

One read failure fails the whole collection, so a single event carbonate
cannot decrypt shows up as every listing returning 500. The log names it.

### Asking carbonate directly

`carbonate calendars -events` reads straight from Proton, past every DAV
endpoint and every cache — the clearest answer to "is this even arriving?".
It cannot run while `carbonate serve` has the session, though:

```
carbonate is already running and using this session; stop it first
```

So either stop the server first, or ask the running one over its own endpoint:

```sh
curl -su 'you@proton.me:BRIDGE_PASSWORD' \
  -X PROPFIND -H 'Depth: 0' -H 'Content-Type: application/xml' \
  --data '<propfind xmlns="DAV:" xmlns:cs="http://calendarserver.org/ns/"><prop><cs:getctag/></prop></propfind>' \
  http://127.0.0.1:8080/caldav/principal/calendars/<TOKEN>/
```

The change token moves whenever the calendar does. If it moves after you add
an event in the Proton web app, carbonate is seeing it and the client is the
problem.

## GNOME Calendar

**Calendars → Add calendar → Add from web**, then the URL, your Proton address
and the bridge password.

If the calendar appears but is read-only, remove it and add it again: Evolution
caches what a server said it could do the first time it asked. The same is true
of `getctag`: one failed request and Evolution stops asking for the rest of the
session, so after upgrading carbonate, re-add the calendar rather than only
restarting it.

### New events take up to half an hour to appear

CalDAV has no push. The client polls, and **Add from web** leaves the interval
at Evolution's default — half an hour. Writing an event is immediate, because
that is a request the client makes; an event made in the Proton web app waits
for the next poll. **Calendars → Synchronize** forces one.

GNOME Calendar has no setting for the interval, so it has to go in the source
file Evolution keeps for the calendar, in `~/.config/evolution/sources/`:

```ini
[Refresh]
Enabled=true
IntervalMinutes=2
```

A short interval is cheap: carbonate answers a poll that found nothing with one
request to Proton, and decrypts nothing. Evolution picks the file up without a
restart.

## GNOME Contacts

GNOME Contacts has no dialog for adding an address book. It shows whatever
Evolution Data Server knows about, and EDS reads that from files in
`~/.config/evolution/sources/`.

Either add the address book in Evolution — both apps share the same EDS, so it
appears in Contacts — or write the file yourself:

```ini
# ~/.config/evolution/sources/carbonate-contacts.source
[Data Source]
DisplayName=Proton Contacts
Enabled=true
Parent=carddav-stub

[Authentication]
Host=127.0.0.1
Method=plain/password
Port=8080
ProxyUid=system-proxy
RememberPassword=true
User=you@proton.me

[Security]
Method=none

[WebDAV Backend]
AvoidIfmatch=false
DisplayName=Proton Contacts
ResourcePath=/carddav/principal/contacts/default/
Order=4294967295
Timeout=30

[Address Book]
BackendName=carddav
Order=0

[Offline]
StaySynchronized=true

[Refresh]
Enabled=true
IntervalMinutes=30
```

`chmod 600` it. EDS watches the directory and picks it up without a restart;
the bridge password is asked for on first use and kept in the keyring.

The equivalent for a calendar uses `Parent=caldav-stub`, a `[Calendar]` group
with `BackendName=caldav`, and the calendar's own `ResourcePath` — which you
can read from the PROPFIND response, or from an entry GNOME Calendar already
made.

## Flatpak clients

GNOME Calendar and Contacts as flatpaks still use the host's EDS, so the files
above are the right place regardless of how the app was installed.
