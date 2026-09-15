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

// compat corrects go-webdav's responses and supplies the properties it lacks.
func compat(next http.Handler, ctag ctagFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantCTag := false

		// go-webdav has no getctag, so the request is answered for it here.
		// Asking go-webdav for a property it does not know would earn a 404
		// propstat that then has to be unpicked, so the name is removed from
		// the request instead and the answer added to the response.
		if r.Method == "PROPFIND" && r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				var stripped []byte

				stripped, wantCTag = stripCTag(body)

				r.Body = io.NopCloser(bytes.NewReader(stripped))
				r.ContentLength = int64(len(stripped))
			}
		}

		rec := &recorder{ResponseWriter: w, method: r.Method}

		next.ServeHTTP(rec, r)

		if wantCTag && ctag != nil {
			if value, err := ctag(r.Context(), r.URL.Path); err == nil {
				rec.body = *bytes.NewBuffer(injectCTag(rec.body.Bytes(), r.URL.Path, value))
			}
		}

		rec.flush()
	})
}

var (
	// ctagPattern matches a getctag element, paired or self-closing, with or
	// without a namespace prefix.
	ctagPattern = regexp.MustCompile(`(?s)<([\w-]+:)?getctag\b[^>]*(/>|>.*?</([\w-]+:)?getctag>)`)

	// emptyPropPattern matches a prop element left with no children.
	emptyPropPattern = regexp.MustCompile(`(?s)<([\w-]+:)?prop\b[^>]*>\s*</([\w-]+:)?prop>`)

	// responsePattern matches one response element of a multistatus.
	responsePattern = regexp.MustCompile(`(?s)<([\w-]+:)?response\b[^>]*>.*?</([\w-]+:)?response>`)

	hrefPattern = regexp.MustCompile(`(?s)<([\w-]+:)?href\b[^>]*>(.*?)</([\w-]+:)?href>`)
)

// stripCTag removes getctag from a PROPFIND body, reporting whether it was
// there. A prop element left empty gets resourcetype instead, since a request
// for nothing at all is not one go-webdav will answer.
func stripCTag(body []byte) ([]byte, bool) {
	if !ctagPattern.Match(body) {
		return body, false
	}

	out := ctagPattern.ReplaceAll(body, nil)

	out = emptyPropPattern.ReplaceAllFunc(out, func(match []byte) []byte {
		i := bytes.Index(match, []byte(">"))

		return append(append(append([]byte{}, match[:i+1]...), []byte(`<resourcetype xmlns="DAV:"/>`)...), match[i+1:]...)
	})

	return out, true
}

// injectCTag adds the getctag property to the response describing collection.
//
// Only the collection carries a ctag; the events inside it must be left alone,
// or a client would treat each one as a collection that never changes.
func injectCTag(body []byte, collection, value string) []byte {
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

		propstat := fmt.Sprintf(
			`<propstat xmlns="DAV:"><prop xmlns="DAV:"><getctag xmlns="http://calendarserver.org/ns/">%s</getctag></prop><status>HTTP/1.1 200 OK</status></propstat>`,
			html.EscapeString(value))

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
