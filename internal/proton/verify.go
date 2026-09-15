package proton

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	api "github.com/ProtonMail/go-proton-api"
)

// ErrVerificationNeeded means Proton wants a human to prove they are one
// before it will accept the login, and nothing supplied a solved token.
var ErrVerificationNeeded = errors.New("Proton asked for human verification, which this way of logging in cannot answer")

// verifyBaseURL is where Proton hosts the challenge.
const verifyBaseURL = "https://verify.proton.me/"

// humanVerification returns the challenge Proton is asking for, or nil if the
// failure was something else.
//
// The details ride on the API error rather than on the response, so they are
// lost if the error is wrapped without care.
func humanVerification(err error) *api.APIHVDetails {
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) {
		return nil
	}

	details, detailsErr := apiErr.GetHVDetails()
	if detailsErr != nil {
		return nil
	}

	return details
}

// verificationURL is the page a person opens to answer the challenge.
//
// The token in the query identifies the challenge; solving it yields a
// different token, which is what the login is retried with.
func verificationURL(details *api.APIHVDetails) string {
	query := url.Values{}
	query.Set("token", details.Token)
	query.Set("methods", strings.Join(details.Methods, ","))

	// Not embedded: carbonate has no browser to host the page in, so the
	// person opens it themselves and brings the answer back.
	query.Set("embed", "false")

	return verifyBaseURL + "?" + query.Encode()
}

// solved turns a token a person brought back into what the retry needs.
func solved(details *api.APIHVDetails, token string) *api.APIHVDetails {
	methods := details.Methods
	if len(methods) == 0 {
		// captcha is what Proton asks for in practice, and sending nothing
		// would have the header say the token is of no particular kind.
		methods = []string{"captcha"}
	}

	return &api.APIHVDetails{Methods: methods, Token: strings.TrimSpace(token)}
}

// describeVerification explains what is being asked, for a prompt.
func describeVerification(details *api.APIHVDetails) string {
	return fmt.Sprintf("Proton wants human verification (%s). Open this, solve it, and paste the token it gives you:\n  %s",
		strings.Join(details.Methods, ", "), verificationURL(details))
}
