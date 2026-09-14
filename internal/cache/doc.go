// Package cache holds decrypted calendar events and contacts on disk, keyed by
// a URL-safe path token so DAV requests resolve to Proton IDs in O(1).
//
// Contents are plaintext user data: files must be written 0600.
package cache
