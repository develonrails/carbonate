package proton

import (
	"errors"
	"net"

	api "github.com/ProtonMail/go-proton-api"
)

// Offline reports whether err means Proton could not be reached at all, as
// opposed to Proton answering and saying no.
//
// The difference decides what to do next. Unreachable is what a machine says
// in the seconds after login, before the network is up, and the answer is to
// wait and try again. A refusal — a revoked session, a refresh token Proton no
// longer accepts — will be the same refusal on every attempt, and needs a
// person.
//
// go-proton-api reports the two paths differently: an authenticated call
// wraps a dropped connection in its own NetError, while refreshing a session
// returns the transport's error as it came — a failed DNS lookup or a refused
// connection, both of them net.Error.
func Offline(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, new(api.NetError)) {
		return true
	}

	var netErr net.Error

	return errors.As(err, &netErr)
}
