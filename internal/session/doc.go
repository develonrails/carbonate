// Package session stores and refreshes Proton credentials.
//
// The session is encrypted at rest with a locally generated bridge password
// and is a long-lived credential. Transparent token refresh lives here; it is
// the piece both hydroxide and protoxide are missing, and the main reason
// carbonate exists.
package session
