package caldav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
)

// syncFunc answers a sync-collection report for a collection.
type syncFunc func(ctx context.Context, path, token string, wantData bool) (syncResult, error)

// calendar-query and calendar-multiget are go-webdav's; sync-collection is
// answered by serveSync below. Nothing is advertised that is not served.
const supportedReportSet = `<supported-report-set xmlns="DAV:">` +
	`<supported-report><report><calendar-query xmlns="urn:ietf:params:xml:ns:caldav"/></report></supported-report>` +
	`<supported-report><report><calendar-multiget xmlns="urn:ietf:params:xml:ns:caldav"/></report></supported-report>` +
	`<supported-report><report><sync-collection/></report></supported-report>` +
	`</supported-report-set>`

var (
	// syncRequestPattern recognises a sync-collection report, which go-webdav
	// would otherwise reject as an unknown kind of REPORT.
	syncRequestPattern = regexp.MustCompile(`(?s)<([\w-]+:)?sync-collection\b`)

	// syncTokenPattern extracts the client's token, which is absent on a
	// first sync.
	syncTokenPattern = regexp.MustCompile(`(?s)<([\w-]+:)?sync-token\b[^>]*>(.*?)</([\w-]+:)?sync-token>`)

	// calendarDataPattern tells whether the client wants the events
	// themselves or only their tags.
	calendarDataPattern = regexp.MustCompile(`(?s)<([\w-]+:)?calendar-data\b`)
)

// serveSync answers a sync-collection report, reporting whether it did.
//
// go-webdav 0.7.0 handles calendar-query and calendar-multiget and rejects
// anything else, so this has to be intercepted before it gets there.
func serveSync(w http.ResponseWriter, r *http.Request, sync syncFunc) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return false
	}

	// Put the body back for the handler behind us, which will need it if this
	// is some other report.
	r.Body = io.NopCloser(bytes.NewReader(body))

	if !syncRequestPattern.Match(body) {
		return false
	}

	token := ""
	if m := syncTokenPattern.FindSubmatch(body); m != nil {
		token = parseSyncToken(string(m[2]))
	}

	result, err := sync(r.Context(), r.URL.Path, token, calendarDataPattern.Match(body))
	if err != nil {
		// go-webdav keeps its error rendering to itself, so say it plainly.
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return true
	}

	answer := result.multistatus()

	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(answer)))
	w.WriteHeader(http.StatusMultiStatus)
	w.Write([]byte(answer))

	return true
}
