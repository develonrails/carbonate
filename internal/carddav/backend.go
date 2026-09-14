// Package carddav exposes Proton Contacts over CardDAV.
package carddav

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-vcard"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/carddav"

	"github.com/develonrails/carbonate/internal/cache"
	"github.com/develonrails/carbonate/internal/contacts"
	"github.com/develonrails/carbonate/internal/proton"
)

// Paths served. go-webdav derives a resource's kind from path depth, so the
// principal must sit at depth 1 and the home set at 2. Proton has a single,
// unnamed collection of contacts, so the address book below is a fixed one.
const (
	prefix        = "/carddav"
	principalPath = prefix + "/principal/"
	homeSetPath   = principalPath + "contacts/"
	bookPath      = homeSetPath + "default/"
	bookName      = "Proton Contacts"
)

// Backend serves one Proton account's contacts.
type Backend struct {
	conn  *proton.Conn
	cache *cache.Events[contacts.Contact]

	// names maps a client-chosen resource path to the UID stored there.
	// CardDAV lets the client name the resource, and Proton knows only the UID.
	mu    sync.RWMutex
	names map[string]string
}

func New(conn *proton.Conn, ttl time.Duration) *Backend {
	return &Backend{
		conn:  conn,
		cache: cache.New[contacts.Contact](ttl),
		names: make(map[string]string),
	}
}

// Handler returns an http.Handler serving CardDAV for this backend.
func (b *Backend) Handler() http.Handler {
	return &carddav.Handler{Backend: b, Prefix: prefix}
}

func (b *Backend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principalPath, nil
}

func (b *Backend) AddressBookHomeSetPath(ctx context.Context) (string, error) {
	return homeSetPath, nil
}

func (b *Backend) ListAddressBooks(ctx context.Context) ([]carddav.AddressBook, error) {
	return []carddav.AddressBook{{
		Path:            bookPath,
		Name:            bookName,
		MaxResourceSize: 100 * 1024,
	}}, nil
}

func (b *Backend) GetAddressBook(ctx context.Context, p string) (*carddav.AddressBook, error) {
	if strings.TrimSuffix(path.Clean(p), "/")+"/" != bookPath {
		return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no such address book: %s", p))
	}

	books, err := b.ListAddressBooks(ctx)
	if err != nil {
		return nil, err
	}

	return &books[0], nil
}

// Proton has exactly one collection of contacts, which carbonate neither
// creates nor removes.
func (b *Backend) CreateAddressBook(ctx context.Context, ab *carddav.AddressBook) error {
	return webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("Proton has a single address book, which cannot be created"))
}

func (b *Backend) DeleteAddressBook(ctx context.Context, p string) error {
	return webdav.NewHTTPError(http.StatusForbidden, fmt.Errorf("Proton's address book cannot be deleted"))
}

func (b *Backend) list(ctx context.Context) ([]contacts.Contact, error) {
	return b.cache.Get(ctx, "contacts", func(ctx context.Context) ([]contacts.Contact, error) {
		return contacts.List(ctx, b.conn)
	})
}

func objectPath(uid string) string {
	return bookPath + url.PathEscape(uid) + ".vcf"
}

// objectUID returns the UID of the contact stored at a path, preferring a name
// the client chose when it wrote there.
func (b *Backend) objectUID(p string) (string, error) {
	clean := path.Clean(p)

	b.mu.RLock()
	uid, ok := b.names[clean]
	b.mu.RUnlock()

	if ok {
		return uid, nil
	}

	base := path.Base(clean)

	name := strings.TrimSuffix(base, ".vcf")
	if name == base {
		return "", webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("not a contact: %s", p))
	}

	unescaped, err := url.PathUnescape(name)
	if err != nil {
		return "", webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("malformed contact path %s: %w", p, err))
	}

	return unescaped, nil
}

// etag changes whenever Proton reports the contact as edited.
func etag(c contacts.Contact) string {
	sum := sha256.Sum256([]byte(c.ID))

	return strconv.FormatInt(c.Modified.Unix(), 10) + "-" + hex.EncodeToString(sum[:4])
}

func object(c contacts.Contact) carddav.AddressObject {
	return carddav.AddressObject{
		Path:    objectPath(c.UID),
		ModTime: c.Modified,
		ETag:    etag(c),
		Card:    c.Card,
	}
}

func (b *Backend) ListAddressObjects(ctx context.Context, p string, req *carddav.AddressDataRequest) ([]carddav.AddressObject, error) {
	all, err := b.list(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]carddav.AddressObject, 0, len(all))
	for _, c := range all {
		out = append(out, object(c))
	}

	return out, nil
}

func (b *Backend) GetAddressObject(ctx context.Context, p string, req *carddav.AddressDataRequest) (*carddav.AddressObject, error) {
	uid, err := b.objectUID(p)
	if err != nil {
		return nil, err
	}

	all, err := b.list(ctx)
	if err != nil {
		return nil, err
	}

	for _, c := range all {
		if c.UID != uid {
			continue
		}

		obj := object(c)

		// Answer at the path the client asked for, not the canonical one.
		obj.Path = path.Clean(p)

		return &obj, nil
	}

	return nil, webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no contact with UID %s", uid))
}

func (b *Backend) QueryAddressObjects(ctx context.Context, p string, query *carddav.AddressBookQuery) ([]carddav.AddressObject, error) {
	objects, err := b.ListAddressObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}

	return carddav.Filter(query, objects)
}

func (b *Backend) PutAddressObject(ctx context.Context, p string, card vcard.Card, opts *carddav.PutAddressObjectOptions) (*carddav.AddressObject, error) {
	uid := card.Value(vcard.FieldUID)
	if uid == "" {
		return nil, webdav.NewHTTPError(http.StatusBadRequest, fmt.Errorf("contact has no UID"))
	}

	if _, _, err := contacts.Put(ctx, b.conn, card); err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.names[path.Clean(p)] = uid
	b.mu.Unlock()

	b.cache.Invalidate("contacts")

	return b.GetAddressObject(ctx, p, nil)
}

func (b *Backend) DeleteAddressObject(ctx context.Context, p string) error {
	uid, err := b.objectUID(p)
	if err != nil {
		return err
	}

	deleted, err := contacts.Delete(ctx, b.conn, uid)
	if err != nil {
		return err
	}

	b.mu.Lock()
	delete(b.names, path.Clean(p))
	b.mu.Unlock()

	b.cache.Invalidate("contacts")

	if !deleted {
		return webdav.NewHTTPError(http.StatusNotFound, fmt.Errorf("no contact with UID %s", uid))
	}

	return nil
}
