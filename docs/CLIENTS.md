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
