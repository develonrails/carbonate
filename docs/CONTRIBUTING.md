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

The fake server is not a full stand-in. It predates Proton's anonymous-session
requirement and hardcodes one-password mode, so those two paths are covered by
unit tests rather than end to end — see
[ARCHITECTURE.md](ARCHITECTURE.md#what-proton-actually-does) for what that
means in practice.

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
