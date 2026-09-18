# Connecting a client

`carbonate serve` listens on `127.0.0.1:8080`. Sign in as your Proton address
with the bridge password printed by `carbonate auth`.

| | |
|---|---|
| Calendar | `http://127.0.0.1:8080/caldav/` |
| Contacts | `http://127.0.0.1:8080/carddav/` |

`/.well-known/caldav` and `/.well-known/carddav` both redirect to the right
principal, so a client that only wants a hostname can find its own way.

## GNOME Online Accounts

The shortest route, and the only one that sets up the calendar and the address
book together. **Settings → Online Accounts → Calendar, Contacts and Files**.

| Field | Value |
|---|---|
| Server Address | `http://127.0.0.1:8080/` |
| Username | your Proton address |
| Password | the bridge password |
| Files | leave empty |
| Calendar (CalDAV) | `http://127.0.0.1:8080/caldav/` |
| Contacts (CardDAV) | `http://127.0.0.1:8080/carddav/` |

Three of those matter more than they look.

**Type `http://` yourself.** The field takes a bare host, and what it makes of
one is `https://`. carbonate speaks plain HTTP on the loopback interface, so
the TLS handshake fails and the dialog reports `Cannot find WebDAV endpoint` —
which reads like carbonate is not there at all.

**Fill the two protocol fields.** carbonate serves the calendar and the address
book under different paths, so there is no single address both can be
discovered from.

**Leave Files empty.** carbonate serves no files. GNOME Online Accounts still
records `FilesEnabled=true`, which leaves GNOME Files trying to mount something
that will never answer; turn Files off in the account once it is created.

### Cannot find WebDAV endpoint

GNOME Online Accounts asks the bare address whether it is a WebDAV server, with
`OPTIONS /`, and reads the `DAV` header of the reply. carbonate answers that
header; a version before this one did not, and this dialog was the only place
it showed. Everything below the root was correct and unreachable, because the
client stopped at the first question.

If the dialog still refuses, check that carbonate is running and that the port
matches:

```sh
curl -i -X OPTIONS http://127.0.0.1:8080/
```

The reply should carry `Dav: 1, 3, calendar-access, addressbook`.

### Moving carbonate to another port

Editing the account is not enough. Evolution derives its own sources from the
account, but the running registry keeps the ones it already built, and those
carry the old port — in memory, so nothing on disk shows it. The symptom is
total silence: the client talks to a port nobody is listening on, and reports
nothing.

```sh
pkill -x evolution-source-registry   # D-Bus starts it again
```

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
restart — but note that GNOME Calendar rewrites the file on startup and will
put its own interval back.

## GNOME Contacts

GNOME Contacts has no dialog for adding an address book. It shows whatever
Evolution Data Server knows about, so the address book has to arrive from
somewhere else:

- **GNOME Online Accounts**, above. This is the one to reach for.
- **Evolution**, which has the dialog Contacts lacks. Both share the same EDS,
  so an address book added there appears in Contacts.
- **A source file**, below, for a machine with neither.

### Writing the source file by hand

EDS reads address books from `~/.config/evolution/sources/`. A file placed
there is picked up without a restart.

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

`chmod 600` it.

The catch is the password. A source written by hand has never been given one,
and GNOME Contacts does not ask — it shows an empty address book and stays
quiet. Only an application that prompts can supply it, which is why the two
routes above are worth trying first.

The equivalent for a calendar uses `Parent=caldav-stub`, a `[Calendar]` group
with `BackendName=caldav`, and the calendar's own `ResourcePath` — which you
can read from the PROPFIND response, or from an entry GNOME Calendar already
made.

## Other clients

Evolution, Thunderbird and DAVx5 take the same three values: the URL, your
Proton address, and the bridge password. Nothing about carbonate is specific to
GNOME; it is the GNOME apps that need the most explanation, because they hide
the most.

## Flatpak clients

GNOME Calendar and Contacts as flatpaks still use the host's EDS, so Online
Accounts and the source files above are the right place regardless of how the
app was installed.
