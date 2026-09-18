# Building and testing

Requires Go 1.26 or newer.

```sh
make            # fmtcheck, vet, test, build
make build      # or: go build ./cmd/carbonate
```

## Testing

```sh
make test       # unit and integration tests
make race       # the run that matters for concurrent token refresh
make cover      # coverage summary
```

Auth is covered by integration tests against the fake Proton server that ships
with go-proton-api, so login, key unlocking, token rotation and session
revocation are all exercised without a real account or network access.

`internal/protontest` wraps that server and serves what it does not.

## Keeping the fake Proton honest

**When the real Proton turns out to do something other than what carbonate
assumed, `internal/protontest` has to learn it too.** A fake nobody updates is
worse than no fake at all: it keeps agreeing with the assumption that was
already wrong, and reports confidence it has not earned.

This is not hypothetical. Every bug found against the live account so far came
from an assumption, not from a mistake in the code — and a fake written from
the same assumption would have passed each one.

The contact tests show what that costs and what it buys.
`TestPutThenListReturnsTheContact` writes a contact and reads it back, and it
passes whether carbonate encrypts to the address key or the user key, because
carbonate reads with the same key it wrote with. Only
`TestContactsAreEncryptedToTheUserKey` fails, because it asks the question
Proton will ask: can the user key *alone* open this?

So a test that proves carbonate agrees with itself proves very little. Write
the one that stands where Proton stands.

### What protontest corrects

go-proton-api's fake covers authentication well, and its contact routes are
built on a different model from the real API:

- It creates **one contact per card**, where a contact *is* its set of cards.
  Writing one contact through it yields two.
- It pages the listing with an index that **goes out of range on an empty
  account** — where every test starts, and every new Proton user.
- It has **no bulk export**, which is how carbonate reads an address book, and
  **no delete** at all.

So protontest serves the contact routes itself and proxies the rest. The fake
also predates Proton's anonymous-session requirement and hardcodes
one-password mode, so those two paths are covered by unit tests rather than
end to end — see
[ARCHITECTURE.md](ARCHITECTURE.md#what-proton-actually-does).

There are **no calendar routes at all**, upstream or here. `internal/calendar`
is tested against its own fakes, and the write path is verified by hand
against a live account. That is the largest remaining gap in the test suite.

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

## Building the Flatpak

```sh
flatpak install flathub org.flatpak.Builder org.gnome.Sdk//50 \
    org.freedesktop.Sdk.Extension.golang//25.08
cd build/flatpak
flatpak run org.flatpak.Builder --force-clean --user --install builddir \
    io.github.develonrails.Carbonate.yml
```

Flathub builds with no network, so every dependency is listed in
`build/flatpak/go.mod.yml` as a module zip and unpacked into `vendor/`. After
changing a dependency, regenerate it:

```sh
go install github.com/dennwc/flatpak-go-mod@latest
flatpak-go-mod .                        # writes go.mod.yml and modules.txt
mv go.mod.yml modules.txt build/flatpak/
```

## CI

Three jobs. `test` runs the suite with no C toolchain, which is the point of
the build tag. `gui` builds the window on a container with the GTK headers.
`flatpak` builds the app, keeping both the OSTree repository it exports on the
way and the single-file bundle made from it.

The release job waits on CI rather than running alongside it: a build published
from a commit whose tests then failed is worse than no build at all.

## How a build reaches people

A green run on `main` promotes that build into the Flatpak repository served
from GitHub Pages, and replaces the bundle on the rolling `continuous` release.

The repository is the route people install from; the bundle is a fallback for
anyone who would rather not add a remote. That order matters, and it used to be
the other way round:

- A bundle is a file, not a remote, so `flatpak update` has nothing to pull
  from.
- Every build is the same ref at the same version — `master`, 0.1.0, since
  nothing bumps a version between CI runs — so a second `flatpak install` stops
  at "already installed".
- GNOME Software refuses a bundle outright, failing to invent an origin remote
  for the debug extension it declares.

A repository has none of these, because OSTree tells builds apart by commit
rather than by version. The three symptoms had one cause, and it was the
bundle.

The promotion uses `flatpak build-commit-from`, which commits the new build
into the published repository with the previous release as its parent. A plain
pull would move the ref to a commit with nothing behind it, and a client that
already has the old one has no path from where it is to where that is.

The `gh-pages` branch is force-pushed as a fresh orphan commit each time. The
repository keeps its own history inside OSTree, so a git history of it as well
would grow every clone without bound and serve nobody.

### The signing key

Builds are signed, and the public half travels inside
`carbonate.flatpakrepo`, so adding the remote is also what establishes trust.
The private half lives in the `FLATPAK_GPG_PRIVATE_KEY` repository secret, and
nothing publishes without it — the release job stops if it is unset.

To create one, or to roll it:

```sh
export GNUPGHOME=$(mktemp -d) && chmod 700 "$GNUPGHOME"

gpg --batch --gen-key <<KEY
%no-protection
Key-Type: eddsa
Key-Curve: Ed25519
Key-Usage: sign
Name-Real: carbonate repository signing
Name-Email: carbonate@develonrails.github.io
Expire-Date: 0
%commit
KEY

gpg --export-secret-keys --armor | base64 -w0 |
    gh secret set FLATPAK_GPG_PRIVATE_KEY --repo develonrails/carbonate
```

Rolling it re-signs everything on the next run, but a client that already
trusts the old key will refuse the new signatures: `flatpak remote-delete
carbonate` and add it again. Worth avoiding for that reason alone.

## Dependencies

`go-proton-api` is pinned to a `master` pseudo-version rather than a tag.
Taking `@latest` silently moves it back four years, to v0.4.0, which cannot log
in to the production API at all. [ARCHITECTURE.md](ARCHITECTURE.md#dependencies)
explains why.
