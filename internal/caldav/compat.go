package caldav

import (
	"bytes"
	"net/http"
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

// compat corrects go-webdav's responses.
func compat(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, method: r.Method}

		next.ServeHTTP(rec, r)
		rec.flush()
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
