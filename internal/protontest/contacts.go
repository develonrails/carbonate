package protontest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	api "github.com/ProtonMail/go-proton-api"
)

// contacts is carbonate's stand-in for Proton's contact store.
//
// go-proton-api's fake server has contact routes, and they are not built on
// Proton's model: it creates one contact per card, where a contact *is* the
// set of cards — one signed, one encrypted, sometimes a cleartext one. Writing
// a single contact through it yields two. It also pages the listing with an
// index that goes out of range on an empty account.
//
// Rather than work around a model that disagrees with the real one, this layer
// serves the contact routes itself. Ownership is the point: when Proton turns
// out to behave differently from what carbonate assumed, this is the file that
// has to learn it too.
type contacts struct {
	mu   sync.Mutex
	next int
	byID map[string]*contact
}

type contact struct {
	id       string
	cards    api.Cards
	modified int64
	order    int
}

func newContacts() *contacts {
	return &contacts{byID: make(map[string]*contact)}
}

// sorted returns the contacts in a stable order, so paging means the same
// thing twice running.
func (c *contacts) sorted() []*contact {
	out := make([]*contact, 0, len(c.byID))
	for _, one := range c.byID {
		out = append(out, one)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].order < out[j].order })

	return out
}

// serve dispatches the contact routes. http.ServeMux would resolve
// /contacts/v4/contacts/export against the by-id pattern, so the paths are
// read here instead.
func (s *Server) serveContacts(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/v4")

	switch {
	case rest == "" || rest == "/":
		switch r.Method {
		case http.MethodGet:
			s.listContacts(w, r)
		case http.MethodPost:
			s.createContacts(w, r)
		default:
			http.Error(w, "unsupported", http.StatusMethodNotAllowed)
		}

	case rest == "/contacts/export":
		s.exportContacts(w, r)

	case rest == "/delete":
		s.deleteContacts(w, r)

	default:
		s.getOrUpdateContact(w, r, strings.TrimPrefix(rest, "/"))
	}
}

func (s *Server) listContacts(w http.ResponseWriter, r *http.Request) {
	page, size := paging(r)

	s.contacts.mu.Lock()
	defer s.contacts.mu.Unlock()

	all := s.contacts.sorted()

	out := make([]api.Contact, 0, size)

	for i := page * size; i < len(all) && len(out) < size; i++ {
		out = append(out, api.Contact{
			ContactMetadata: api.ContactMetadata{ID: all[i].id, ModifyTime: all[i].modified},
		})
	}

	writeJSON(w, map[string]any{"Code": 1001, "Contacts": out, "Total": len(all)})
}

func (s *Server) createContacts(w http.ResponseWriter, r *http.Request) {
	var req api.CreateContactsReq

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	s.contacts.mu.Lock()
	defer s.contacts.mu.Unlock()

	responses := make([]api.CreateContactsRes, 0, len(req.Contacts))

	// One contact per entry, however many cards it carries. That is the whole
	// correction: a contact is its set of cards.
	for i, entry := range req.Contacts {
		s.contacts.next++

		one := &contact{
			id:       fmt.Sprintf("contact-%d", s.contacts.next),
			cards:    entry.Cards,
			modified: int64(1000 + s.contacts.next),
			order:    s.contacts.next,
		}

		s.contacts.byID[one.id] = one

		responses = append(responses, api.CreateContactsRes{
			Index: i,
			Response: api.CreateContactResp{
				APIError: api.APIError{Code: 1000},
				Contact: api.Contact{
					ContactMetadata: api.ContactMetadata{ID: one.id, ModifyTime: one.modified},
					ContactCards:    api.ContactCards{Cards: one.cards},
				},
			},
		})
	}

	writeJSON(w, map[string]any{"Code": 1001, "Responses": responses})
}

func (s *Server) getOrUpdateContact(w http.ResponseWriter, r *http.Request, id string) {
	s.contacts.mu.Lock()
	defer s.contacts.mu.Unlock()

	one, ok := s.contacts.byID[id]
	if !ok {
		writeJSON(w, map[string]any{"Code": 2501, "Error": "no such contact"})

		return
	}

	if r.Method == http.MethodPut {
		var req api.UpdateContactReq

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		one.cards = req.Cards
		one.modified++
	}

	writeJSON(w, map[string]any{"Code": 1000, "Contact": api.Contact{
		ContactMetadata: api.ContactMetadata{ID: one.id, ModifyTime: one.modified},
		ContactCards:    api.ContactCards{Cards: one.cards},
	}})
}

func (s *Server) deleteContacts(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	s.contacts.mu.Lock()

	responses := make([]map[string]any, 0, len(req.IDs))

	for _, id := range req.IDs {
		delete(s.contacts.byID, id)

		// 1001 is Proton's own answer to a batch that succeeded, and reading
		// it as an error is a mistake carbonate has made before.
		responses = append(responses, map[string]any{"ID": id, "Response": map[string]any{"Code": 1000}})
	}

	s.contacts.mu.Unlock()

	writeJSON(w, map[string]any{"Code": 1001, "Responses": responses})
}

// exportContacts answers the bulk endpoint carbonate reads an address book
// with. go-proton-api has no call for it, so carbonate builds the request by
// hand and this serves it.
func (s *Server) exportContacts(w http.ResponseWriter, r *http.Request) {
	page, size := paging(r)

	s.contacts.mu.Lock()
	defer s.contacts.mu.Unlock()

	all := s.contacts.sorted()

	type exported struct {
		ID    string
		Cards api.Cards
	}

	out := make([]exported, 0, size)

	for i := page * size; i < len(all) && len(out) < size; i++ {
		out = append(out, exported{ID: all[i].id, Cards: all[i].cards})
	}

	writeJSON(w, map[string]any{"Code": 1001, "Contacts": out})
}

func paging(r *http.Request) (page, size int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("Page"))
	size, _ = strconv.Atoi(r.URL.Query().Get("PageSize"))

	if size <= 0 {
		size = 50
	}

	return page, size
}
