package caldav

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// go-webdav 0.7.0 deviates from the specs in two ways that make a client treat
// the calendar as read-only. Both are corrected on the way out rather than by
// forking the library.

// privilegePattern matches a DAV:privilege element and captures its children.
var privilegePattern = regexp.MustCompile(`(?s)<privilege([^>]*)>(.*?)</privilege>`)

// childPattern matches one child element, either paired or self-closing.
var childPattern = regexp.MustCompile(`(?s)<([\w-]+)([^>/]*)>\s*</[\w-]+>|<([\w-]+)([^>/]*)/>`)

// methodsForCalendar are the methods a calendar collection accepts. go-webdav
// omits PUT, and a client reading the Allow header concludes it cannot write.
var methodsForCalendar = []string{http.MethodPut, http.MethodGet, http.MethodHead}

// ctagFunc returns the change token of the collection at a path.
type ctagFunc func(ctx context.Context, path string) (string, error)

// syncFunc answers a sync-collection report.
type syncFunc func(ctx context.Context, path, token string, wantData bool) (syncResult, error)

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

// supplied are properties go-webdav does not know, which carbonate answers
// itself. Each is removed from the PROPFIND before go-webdav sees it —
// asking for an unknown property earns a 404 propstat that would then have to
// be unpicked — and the answer is added to the response afterwards.
//
// calendar-query and calendar-multiget are go-webdav's; sync-collection is
// answered by serveSync below. Nothing is advertised that is not served.
var supportedReportSet = `<supported-report-set xmlns="DAV:">` +
	`<supported-report><report><calendar-query xmlns="urn:ietf:params:xml:ns:caldav"/></report></supported-report>` +
	`<supported-report><report><calendar-multiget xmlns="urn:ietf:params:xml:ns:caldav"/></report></supported-report>` +
	`<supported-report><report><sync-collection/></report></supported-report>` +
	`</supported-report-set>`

// compat corrects go-webdav's responses and supplies the properties it lacks.
func compat(next http.Handler, ctag ctagFunc, sync syncFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" && sync != nil {
			if handled := serveSync(w, r, sync); handled {
				return
			}
		}

		wantCTag, wantReports := false, false

		if r.Method == "PROPFIND" && r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				var stripped []byte

				stripped, wantCTag = stripProp(body, "getctag")
				stripped, wantReports = stripProp(stripped, "supported-report-set")
				stripped = fillEmptyProp(stripped)

				r.Body = io.NopCloser(bytes.NewReader(stripped))
				r.ContentLength = int64(len(stripped))
			}
		}

		rec := &recorder{ResponseWriter: w, method: r.Method}

		next.ServeHTTP(rec, r)

		if wantReports {
			rec.body = *bytes.NewBuffer(injectProp(rec.body.Bytes(), r.URL.Path, supportedReportSet))
		}

		if wantCTag && ctag != nil {
			if value, err := ctag(r.Context(), r.URL.Path); err == nil {
				rec.body = *bytes.NewBuffer(injectProp(rec.body.Bytes(), r.URL.Path,
					fmt.Sprintf(`<getctag xmlns="http://calendarserver.org/ns/">%s</getctag>`, html.EscapeString(value))))
			}
		}

		rec.flush()
	})
}

var (
	// emptyPropPattern matches a prop element left with no children.
	emptyPropPattern = regexp.MustCompile(`(?s)<([\w-]+:)?prop\b[^>]*>\s*</([\w-]+:)?prop>`)

	// responsePattern matches one response element of a multistatus.
	responsePattern = regexp.MustCompile(`(?s)<([\w-]+:)?response\b[^>]*>.*?</([\w-]+:)?response>`)

	hrefPattern = regexp.MustCompile(`(?s)<([\w-]+:)?href\b[^>]*>(.*?)</([\w-]+:)?href>`)
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

// stripProp removes a named property from a PROPFIND body, reporting whether
// it was there.
func stripProp(body []byte, name string) ([]byte, bool) {
	pattern := regexp.MustCompile(`(?s)<([\w-]+:)?` + regexp.QuoteMeta(name) + `\b[^>]*(/>|>.*?</([\w-]+:)?` + regexp.QuoteMeta(name) + `>)`)

	if !pattern.Match(body) {
		return body, false
	}

	return pattern.ReplaceAll(body, nil), true
}

// fillEmptyProp puts something harmless into a prop element the stripping
// emptied, since a request for nothing at all is not one go-webdav answers.
func fillEmptyProp(body []byte) []byte {
	return emptyPropPattern.ReplaceAllFunc(body, func(match []byte) []byte {
		i := bytes.Index(match, []byte(">"))

		return append(append(append([]byte{}, match[:i+1]...), []byte(`<resourcetype xmlns="DAV:"/>`)...), match[i+1:]...)
	})
}

// injectProp adds a property to the response describing collection.
//
// Only the collection is touched; the events inside it must be left alone, or
// a client would treat each one as a collection of its own.
func injectProp(body []byte, collection, property string) []byte {
	want := strings.TrimSuffix(path.Clean(collection), "/")

	return responsePattern.ReplaceAllFunc(body, func(response []byte) []byte {
		href := hrefPattern.FindSubmatch(response)
		if href == nil {
			return response
		}

		got := strings.TrimSuffix(path.Clean(html.UnescapeString(string(href[2]))), "/")
		if got != want {
			return response
		}

		propstat := `<propstat xmlns="DAV:"><prop xmlns="DAV:">` + property + `</prop><status>HTTP/1.1 200 OK</status></propstat>`

		i := bytes.LastIndex(response, []byte("</"))
		if i < 0 {
			return response
		}

		return append(append(append([]byte{}, response[:i]...), []byte(propstat)...), response[i:]...)
	})
}

// recorder buffers the response so the body can be corrected before it is sent,
// and adjusts headers before they are committed.
type recorder struct {
	http.ResponseWriter

	method string
	status int
	body   bytes.Buffer
	sent   bool
}

func (r *recorder) WriteHeader(status int) {
	r.status = status

	if r.method == http.MethodOptions {
		r.ResponseWriter.Header().Set("Allow", allowWithWrites(r.ResponseWriter.Header().Get("Allow")))
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}

// flush rewrites the buffered body and sends it.
func (r *recorder) flush() {
	body := r.body.Bytes()

	if isXML(r.ResponseWriter.Header().Get("Content-Type")) {
		body = splitPrivileges(body)
	}

	if r.status == 0 {
		r.status = http.StatusOK
	}

	// The buffered body has its own length; the one go-webdav set is stale.
	r.ResponseWriter.Header().Del("Content-Length")

	if !r.sent {
		r.sent = true
		r.ResponseWriter.WriteHeader(r.status)
	}

	r.ResponseWriter.Write(body)
}

func isXML(contentType string) bool {
	return strings.Contains(contentType, "xml")
}

// allowWithWrites adds the write methods a calendar collection supports.
//
// go-webdav answers OPTIONS on a collection with only read and delete methods,
// which reads as "this collection cannot be written to".
func allowWithWrites(allow string) string {
	if allow == "" {
		return strings.Join(methodsForCalendar, ", ")
	}

	present := make(map[string]bool)

	methods := strings.Split(allow, ",")
	for i, m := range methods {
		methods[i] = strings.TrimSpace(m)
		present[methods[i]] = true
	}

	for _, m := range methodsForCalendar {
		if !present[m] {
			methods = append(methods, m)
		}
	}

	return strings.Join(methods, ", ")
}

// splitPrivileges gives each privilege its own element.
//
// RFC 3744 defines DAV:privilege as holding a single privilege, so
// "<privilege><read/><write/></privilege>" — which go-webdav emits — is not
// two privileges but one malformed element. A client that reads only the first
// child sees a read-only calendar.
func splitPrivileges(body []byte) []byte {
	return privilegePattern.ReplaceAllFunc(body, func(match []byte) []byte {
		groups := privilegePattern.FindSubmatch(match)
		attrs, inner := groups[1], groups[2]

		children := childPattern.FindAll(inner, -1)
		if len(children) < 2 {
			return match
		}

		var out bytes.Buffer

		for _, child := range children {
			out.WriteString("<privilege")
			out.Write(attrs)
			out.WriteString(">")
			out.Write(child)
			out.WriteString("</privilege>")
		}

		return out.Bytes()
	})
}
