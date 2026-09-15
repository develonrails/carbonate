package caldav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func syncRequest(token string) string {
	inner := ""
	if token != "" {
		inner = "<d:sync-token>" + token + "</d:sync-token>"
	}

	return `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:">` + inner +
		`<d:sync-level>1</d:sync-level><d:prop><d:getetag/></d:prop></d:sync-collection>`
}

// A client with no token has nothing to compare against, so it is given
// everything — which RFC 6578 provides for.
func TestFirstSyncReturnsEverything(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	got, err := b.sync(context.Background(), path, "", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(got.Changed) != 1 {
		t.Errorf("reported %d members, want 1", len(got.Changed))
	}

	if !got.Resync {
		t.Error("a first sync was not reported as a full listing")
	}

	if got.Token == "" {
		t.Error("no token was issued, so the client can never sync again")
	}
}

func TestSyncReportsAChangedEvent(t *testing.T) {
	fake := newFake()
	fake.changes["token-0"] = []string{"event-1"}
	fake.token = "token-2"

	b := backendWith(fake)
	path := calendarPath(t, b)

	got, err := b.sync(context.Background(), path, "token-0", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(got.Changed) != 1 || len(got.Removed) != 0 {
		t.Fatalf("changed %d, removed %d; want 1 and 0", len(got.Changed), len(got.Removed))
	}

	if got.Resync {
		t.Error("a delta was reported as a full listing")
	}

	if got.Token != "token-2" {
		t.Errorf("token = %q, want the new one", got.Token)
	}
}

// A deletion is the case this design exists for: Proton names the event by
// its own ID, while the client knows it by a path built from the UID, which
// has gone with the event.
func TestSyncReportsADeletionByItsOldPath(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	// Read once, so the event is known before it disappears.
	if _, err := b.ListCalendarObjects(context.Background(), path, nil); err != nil {
		t.Fatalf("ListCalendarObjects: %v", err)
	}

	if _, err := fake.Delete(context.Background(), "cal-1", "meeting@example.com"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	fake.changes["token-1"] = []string{"event-1"}
	fake.token = "token-2"

	got, err := b.sync(context.Background(), path, "token-1", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if len(got.Removed) != 1 {
		t.Fatalf("removed %d members, want 1", len(got.Removed))
	}

	// The path has to be exactly the one the client was given, or it will
	// keep a copy of an event that no longer exists.
	want := objectPath(calendarSegment(path), "meeting@example.com")
	if got.Removed[0] != want {
		t.Errorf("removed %q, want %q — the path the client knew", got.Removed[0], want)
	}
}

// If an event vanished without this process ever having seen it, the client
// may hold a copy that cannot be named. Saying "read everything" is honest;
// silently omitting it would leave a ghost in the client forever.
func TestSyncFallsBackWhenADeletionCannotBeNamed(t *testing.T) {
	fake := newFake()
	fake.changes["token-1"] = []string{"an-event-we-never-saw"}

	b := backendWith(fake)
	path := calendarPath(t, b)

	got, err := b.sync(context.Background(), path, "token-1", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if !got.Resync {
		t.Error("an unnameable deletion was not reported as needing a full read")
	}
}

// A token Proton will not account for means the client has been away too
// long.
func TestSyncFallsBackOnAnUnusableToken(t *testing.T) {
	fake := newFake()
	fake.resyncFrom["ancient"] = true

	b := backendWith(fake)
	path := calendarPath(t, b)

	got, err := b.sync(context.Background(), path, "ancient", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if !got.Resync || len(got.Changed) != 1 {
		t.Errorf("result = %+v, want a full listing", got)
	}
}

func TestSyncIncludesDataWhenAsked(t *testing.T) {
	b := backendWith(newFake())
	path := calendarPath(t, b)

	withData, err := b.sync(context.Background(), path, "", true)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if withData.Changed[0].ICS == "" {
		t.Error("calendar data was asked for and not returned")
	}

	without, err := b.sync(context.Background(), path, "", false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	if without.Changed[0].ICS != "" {
		t.Error("calendar data was returned although only tags were asked for")
	}
}

func TestMultistatusShape(t *testing.T) {
	result := syncResult{
		Changed: []changedMember{{Path: "/c/a.ics", ETag: "1-abc"}},
		Removed: []string{"/c/b.ics"},
		Token:   "token-9",
	}

	got := result.multistatus()

	for _, want := range []string{
		`<href>/c/a.ics</href>`,
		`HTTP/1.1 200 OK`,
		`<href>/c/b.ics</href>`,
		`HTTP/1.1 404 Not Found`,
		`<sync-token>urn:carbonate:token-9</sync-token>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("response is missing %q:\n%s", want, got)
		}
	}
}

// RFC 6578 wants the token to be a URI, and it must come back recognisable.
func TestSyncTokenRoundTrips(t *testing.T) {
	result := syncResult{Token: "cursor-1"}

	issued := result.multistatus()

	start := strings.Index(issued, "<sync-token>") + len("<sync-token>")
	end := strings.Index(issued, "</sync-token>")

	if got := parseSyncToken(issued[start:end]); got != "cursor-1" {
		t.Errorf("parsed %q, want the cursor back", got)
	}
}

// A token issued by something else must not be mistaken for a cursor, or the
// delta would be computed from nonsense.
func TestForeignSyncTokenIsIgnored(t *testing.T) {
	if got := parseSyncToken("http://example.com/ns/sync/12345"); got == "12345" {
		t.Error("a foreign token was read as one of ours")
	}
}

func TestServeSyncAnswersTheReport(t *testing.T) {
	fake := newFake()
	b := backendWith(fake)
	path := calendarPath(t, b)

	handler := b.Handler()

	req := httptest.NewRequest("REPORT", path, strings.NewReader(syncRequest("")))
	req.Header.Set("Content-Type", "application/xml")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("status = %d, want 207", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)

	if !strings.Contains(string(body), "<sync-token>") {
		t.Errorf("no token was issued:\n%s", body)
	}

	if !strings.Contains(string(body), ".ics") {
		t.Errorf("no members were reported:\n%s", body)
	}
}

// Other reports must still reach go-webdav untouched.
func TestOtherReportsArePassedThrough(t *testing.T) {
	reached := false

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})

	query := `<?xml version="1.0"?><c:calendar-query xmlns:c="urn:ietf:params:xml:ns:caldav"/>`

	req := httptest.NewRequest("REPORT", "/caldav/principal/calendars/abc/", strings.NewReader(query))
	rec := httptest.NewRecorder()

	compat(inner, nil, func(context.Context, string, string, bool) (syncResult, error) {
		t.Error("a calendar-query was treated as a sync-collection")

		return syncResult{}, nil
	}).ServeHTTP(rec, req)

	if !reached {
		t.Error("the report never reached the handler behind the middleware")
	}
}
