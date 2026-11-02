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

// ErrVerificationRefused means Proton asked again after the answer was sent.
// Most often the puzzle was not finished before the login carried on.
var ErrVerificationRefused = errors.New("Proton did not accept the verification; solve the puzzle until the page says verification is complete, then sign in again")

// verifyBaseURL is where Proton hosts the challenge.
const verifyBaseURL = "https://verify.proton.me/"

// CaptchaConfirmer is optional for a Prompter: one that can wait for a person
// to say they solved the puzzle lets the login reuse the challenge token, and
// one that cannot is asked for a token through Verify instead.
//
// Exported so a front end can assert it implements this. The check is a type
// assertion at run time, so a signature that drifts falls back without a word.
type CaptchaConfirmer interface {
	ConfirmCaptcha(message string) error
}

func verificationToken(details *api.APIHVDetails, p Prompter) (string, error) {
	if len(details.Methods) == 1 && details.Methods[0] == "captcha" {
		if confirmer, ok := p.(CaptchaConfirmer); ok {
			message := fmt.Sprintf("Proton wants human verification (captcha). Open this link and complete the puzzle:\n  %s\n\nNo verification code is needed.", verificationURL(details))
			if err := confirmer.ConfirmCaptcha(message); err != nil {
				return "", err
			}
			return details.Token, nil
		}
	}
	return p.Verify(describeVerification(details))
}

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
// In standalone mode, Proton submits the CAPTCHA proof for the original
// challenge token. The retry returns that same token after the person solves it.
func verificationURL(details *api.APIHVDetails) string {
	query := url.Values{}
	query.Set("token", details.Token)
	query.Set("methods", strings.Join(details.Methods, ","))

	// Omit embed entirely: Proton treats it as a string, so even "false"
	// selects embedded mode and prevents the standalone proof submission.

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
	if len(details.Methods) == 1 && details.Methods[0] == "captcha" {
		return fmt.Sprintf("Proton wants human verification (captcha). Open this link and complete the puzzle:\n  %s\n\nNo code is emailed or displayed by the CAPTCHA page. After it says verification is complete, paste this token into Carbonate:\n  %s",
			verificationURL(details), details.Token)
	}

	return fmt.Sprintf("Proton wants human verification (%s). Open this, solve it, and paste the token it gives you:\n  %s",
		strings.Join(details.Methods, ", "), verificationURL(details))
}
