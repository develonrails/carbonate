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

## GNOME Calendar

**Calendars → Add calendar → Add from web**, then the URL, your Proton address
and the bridge password.

If the calendar appears but is read-only, remove it and add it again: Evolution
caches what a server said it could do the first time it asked.

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
