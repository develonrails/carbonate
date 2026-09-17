// Package davcompat corrects what go-webdav 0.7.0 gets wrong and supplies the
// properties it lacks, for both of the protocols carbonate serves.
//
// None of this is carbonate's choice. Each is a place where the library's
// output leaves a client with the wrong idea, fixed on the way out rather
// than by forking.
package davcompat

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

// writeMethods are the methods a calendar collection accepts. go-webdav
// omits PUT, and a client reading the Allow header concludes it cannot write.
var writeMethods = []string{http.MethodPut, http.MethodGet, http.MethodHead}

// CTagFunc returns the change token of the collection at a path.
type CTagFunc func(ctx context.Context, path string) (string, error)

// SyncFunc answers a sync-collection report, reporting whether it did.
type SyncFunc func(w http.ResponseWriter, r *http.Request) bool

// Options are what a protocol supplies on top of the shared corrections.
type Options struct {
	// SupportedReports is the XML for supported-report-set. Empty means the
	// property is not answered, and nothing is claimed that is not served.
	SupportedReports string

	// CTag supplies the collection's change token, if there is one.
	CTag CTagFunc

	// ServeSync answers a sync-collection report, if the protocol has one.
	ServeSync SyncFunc
}

// supplied are properties go-webdav does not know, which carbonate answers
// itself. Each is removed from the PROPFIND before go-webdav sees it —
// asking for an unknown property earns a 404 propstat that would then have to
// be unpicked — and the answer is added to the response afterwards.
//

// Wrap corrects go-webdav's responses and supplies the properties it lacks.
func Wrap(next http.Handler, opts Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "REPORT" && opts.ServeSync != nil {
			if handled := opts.ServeSync(w, r); handled {
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

		if wantReports && opts.SupportedReports != "" {
			rec.body = *bytes.NewBuffer(injectProp(rec.body.Bytes(), r.URL.Path, opts.SupportedReports))
		}

		if wantCTag && opts.CTag != nil {
			if value, err := opts.CTag(r.Context(), r.URL.Path); err == nil {
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

	// propstatPattern matches one propstat element of a response.
	propstatPattern = regexp.MustCompile(`(?s)<([\w-]+:)?propstat\b[^>]*>.*?</([\w-]+:)?propstat>`)

	// statusPattern matches a status element and captures its status line.
	statusPattern = regexp.MustCompile(`(?s)<([\w-]+:)?status\b[^>]*>(.*?)</([\w-]+:)?status>`)

	// propClosePattern matches the end of a prop element.
	propClosePattern = regexp.MustCompile(`</([\w-]+:)?prop>`)
)

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
//
// The property joins the response's existing successful propstat rather than
// arriving in one of its own. A propstat per property is legal, and clients
// that read the whole response are none the wiser, but Evolution Data Server
// — and so GNOME Calendar — stops at the first one it is shown. A ctag it
// never reads is a calendar that never refreshes, so the answer has to be
// where the first look lands.
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

		if merged, ok := mergeIntoPropstat(response, property); ok {
			return merged
		}

		// Nothing succeeded for this resource, so there is no prop list to
		// join. Start one, ahead of whatever did not succeed.
		propstat := `<propstat xmlns="DAV:"><prop xmlns="DAV:">` + property + `</prop><status>HTTP/1.1 200 OK</status></propstat>`

		end := hrefPattern.FindIndex(response)
		if end == nil {
			return response
		}

		return concat(response[:end[1]], []byte(propstat), response[end[1]:])
	})
}

// mergeIntoPropstat puts property into the prop list of the response's first
// successful propstat, reporting whether there was one.
func mergeIntoPropstat(response []byte, property string) ([]byte, bool) {
	for _, span := range propstatPattern.FindAllIndex(response, -1) {
		propstat := response[span[0]:span[1]]

		status := statusPattern.FindSubmatch(propstat)
		if status == nil || !isSuccess(string(status[2])) {
			continue
		}

		closing := propClosePattern.FindIndex(propstat)
		if closing == nil {
			continue
		}

		at := span[0] + closing[0]

		return concat(response[:at], []byte(property), response[at:]), true
	}

	return nil, false
}

// isSuccess reads a status line the way a client does: 2xx and nothing else.
func isSuccess(line string) bool {
	fields := strings.Fields(strings.TrimSpace(html.UnescapeString(line)))
	if len(fields) < 2 {
		return false
	}

	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return false
	}

	return code >= 200 && code < 300
}

// concat joins byte slices into a new one, so that appending to a subslice of
// the original cannot overwrite what follows it.
func concat(parts ...[]byte) []byte {
	var out []byte

	for _, part := range parts {
		out = append(out, part...)
	}

	return out
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
		return strings.Join(writeMethods, ", ")
	}

	present := make(map[string]bool)

	methods := strings.Split(allow, ",")
	for i, m := range methods {
		methods[i] = strings.TrimSpace(m)
		present[methods[i]] = true
	}

	for _, m := range writeMethods {
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
