# Running carbonate

Everything here is about the bridge itself rather than the clients that connect
to it. For those, see [SETUP.md](SETUP.md).

## Staying up to date

carbonate is installed from a Flatpak repository, so it updates like anything
else:

```sh
flatpak update io.github.develonrails.Carbonate
```

GNOME Software offers the same update; there is nothing to download by hand.
Every green CI run on `main` publishes a new build, so an update can bring
anything that has landed since — see the warning at the top of the
[README](../README.md).

Stop the bridge before updating. A running `carbonate serve` holds the session
lock, and the replaced binary will not take over from it until it is restarted
anyway.

### Moving off a bundle

If you installed the single-file bundle rather than the repository, none of the
above works, and it fails quietly: `flatpak update` answers **"Nothing to
update."** and leaves you on the build you have. There is nothing to update
from — a bundle install invents a hidden origin with no URL behind it.

Adding the repository is not enough on its own either:

```
error: io.github.develonrails.Carbonate/x86_64/master is already installed
from remote carbonate-origin
```

Point the existing install at the repository instead:

```sh
flatpak install --user --reinstall \
    carbonate io.github.develonrails.Carbonate
```

That switches the origin, after which ordinary updates work. It is worth
checking which one you have — a bundle install reports its hidden origin, and
an update that never arrives looks exactly like a project that stopped moving:

```sh
flatpak info --user io.github.develonrails.Carbonate | grep Origin
```

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

## Environment

| | |
|---|---|
| `CARBONATE_BRIDGE_PASSWORD` | open the session without a prompt, for running unattended |
| `CARBONATE_NO_INVITATIONS=1` | record attendees without mailing them |
| `CARBONATE_DEBUG=1` | dump every Proton request and response — **prints access tokens** |
| `CARBONATE_APP_VERSION` | override the product string sent to Proton |

## Invitations are mail

Inviting someone to an event mails them, because at Proton that is what an
invitation is — the event alone is invisible to anyone who does not already
know it is there. Only a change to the guest list is announced, so re-syncing a
calendar does not mail anybody.

## Watching what the bridge is doing

`carbonate serve -log` reports every DAV request and every change Proton
sends. Without it the bridge is silent: a request that failed is visible only
to the client that received the failure, and a client reports that as a
calendar that will not update, or as nothing at all. The window shows the same
thing under **Activity**.

```console
$ carbonate serve -log
15:04:38 PROPFIND  /caldav/principal/calendars/12081a3d…/ 207 6ms
15:04:38 REPORT    /caldav/principal/calendars/12081a3d…/ 207 8ms
carbonate: calendar 7WcS1DSk…: Proton reports a change (LU4nT24F…)
carbonate: calendar 7WcS1DSk…: read 14 events from Proton
carbonate: calendar 7WcS1DSk…: created meeting@example.com
```

Read it in two halves. Lines with a time and a status are what your client
asked for; lines beginning `carbonate:` are what Proton sent. An idle bridge
shows only the former, because a poll that found nothing is not worth a line.

That split is what makes the common reports separable:

| What the log shows | What it means |
|---|---|
| No lines at all | the client is not polling — see [SETUP.md](SETUP.md) |
| Requests, but never `Proton reports a change` | the change is not reaching carbonate |
| `reports a change`, client still empty | the client is not acting on it |
| A 4xx or 5xx with a reason | that reason is the bug |

**A new event can take half an hour to appear, and that is normal.** CalDAV has
no push: the client polls, and most default to a long interval. That is the
most common report by far, and [SETUP.md](SETUP.md) explains how to shorten it.

A contact or an event carbonate cannot serve is named in the log and then left
out, rather than failing the listing:

```
carbonate: calendar 7WcS1DSk…: event G4Vr95GC… cannot be decrypted, so it is not being served
carbonate: calendar 7WcS1DSk…: event party@example.com cannot be served: ical: malformed content line
```

One unreadable item must not hide every readable one. That distinction is the
whole of issue #18: an error fails the collection, and Evolution shows a failed
collection as an empty calendar — indistinguishable from Proton having sent
nothing at all. A calendar is now one event short instead.

It is still worth chasing the line rather than living with it, since the event
is in the Proton web app and not in your client.

### Events signed by someone else

```
carbonate: calendar 7WcS1DSk…: read 14 events from Proton (3 signed by a key we do not hold)
```

This one is normal and needs nothing done about it. A signature says who wrote
an event, and carbonate holds only your own address keys — so an event another
member of a shared calendar added, or one that arrived as an invitation, is
signed with a key it cannot check. Those events decrypt fine and are served;
the count is there so they are not silent.

`carbonate calendars -events` marks each one `(signature not verified)`.

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
problem. The address book answers the same property at
`/carddav/principal/contacts/default/`.

`CARBONATE_DEBUG=1` dumps the Proton API traffic underneath all of this. It is
a last resort rather than a first one — it prints access tokens and every
encrypted payload.
