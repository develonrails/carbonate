// Package proton wraps Proton's private API: SRP authentication, key
// unlocking, and the event-loop endpoint used for delta sync.
//
// The intent is to build on go-proton-api (the library underneath the official
// Proton Mail Bridge) rather than reimplementing SRP and OpenPGP handling.
package proton
