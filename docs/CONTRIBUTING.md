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
`flatpak` builds the bundle.

A green run on `main` publishes that bundle to the rolling `continuous`
release, replacing whatever was there. The release job waits on CI rather than
running alongside it: a bundle published from a commit whose tests then failed
is worse than no bundle at all.

## Dependencies

`go-proton-api` is pinned to a `master` pseudo-version rather than a tag.
Taking `@latest` silently moves it back four years, to v0.4.0, which cannot log
in to the production API at all. [ARCHITECTURE.md](ARCHITECTURE.md#dependencies)
explains why.
