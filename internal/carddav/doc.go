// Package carddav implements a CardDAV backend over Proton Contacts.
//
// Proton contacts are already vCards, split into cards by protection level
// (cleartext, signed, encrypted+signed) under the user's address key.
package carddav
