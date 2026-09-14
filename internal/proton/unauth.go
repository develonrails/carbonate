package proton

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	api "github.com/ProtonMail/go-proton-api"
	"github.com/go-resty/resty/v2"
)

// unauthSession is the anonymous session Proton hands out before a login.
//
// Since go-proton-api v0.4.0 the API changed: /auth/v4/info now answers 401
// "Invalid access token" unless the request belongs to a session, even though
// logging in is by definition unauthenticated. go-proton-api does not create
// one itself, so carbonate does.
type unauthSession struct {
	UID         string
	AccessToken string
}

// newUnauthSession asks Proton for an anonymous session.
//
// It returns (nil, nil) when the server has no such endpoint. The fake Proton
// server used in tests predates the change and treats /auth/v4/info as
// unauthenticated, so requiring a session there would break every test against
// it without saying anything about production.
func newUnauthSession(ctx context.Context, hostURL, appVersion string) (*unauthSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hostURL+"/auth/v4/sessions", strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("building session request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-pm-appversion", appVersion)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting anonymous session: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusMethodNotAllowed {
		return nil, nil
	}

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("requesting anonymous session: unexpected status %s", res.Status)
	}

	var body struct {
		UID         string
		AccessToken string
	}

	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decoding anonymous session: %w", err)
	}

	if body.UID == "" || body.AccessToken == "" {
		return nil, fmt.Errorf("anonymous session is missing a UID or access token")
	}

	return &unauthSession{UID: body.UID, AccessToken: body.AccessToken}, nil
}

// attach makes every otherwise-unidentified request carry this session.
//
// Requests issued by an authenticated Client already set x-pm-uid and a bearer
// token, and must not be overwritten; only manager-level calls such as
// /auth/v4/info are anonymous.
func (s *unauthSession) attach(m *api.Manager) {
	m.AddPreRequestHook(func(_ *resty.Client, r *resty.Request) error {
		if r.Header.Get("x-pm-uid") == "" && r.Token == "" {
			r.SetHeader("x-pm-uid", s.UID)
			r.SetAuthToken(s.AccessToken)
		}

		return nil
	})
}

// hostURL is the Proton API carbonate talks to.
func hostURL() string {
	if u := apiURL(); u != "" {
		return u
	}

	return api.DefaultHostURL
}

// newAnonymousManager returns a Manager able to perform a login.
func newAnonymousManager(ctx context.Context) (*api.Manager, error) {
	m := newManager()

	sess, err := newUnauthSession(ctx, hostURL(), appVersion())
	if err != nil {
		m.Close()
		return nil, err
	}

	if sess != nil {
		sess.attach(m)
	}

	return m, nil
}
