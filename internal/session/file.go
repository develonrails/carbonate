package session

import "sync"

// File is a session together with the place it is stored, so that a rotated
// token is written down as a matter of course rather than as something the
// caller has to remember.
//
// Proton rotates the refresh token on every use and discards the old one. The
// interface this satisfies used to be a callback, and a callback that does
// nothing compiles, runs, and leaves the stored session holding a token Proton
// has already thrown away — a failure that only shows up later, as an account
// that appears broken. Three such callbacks were written during this project
// before the shape was changed.
type File struct {
	mu       sync.Mutex
	path     string
	password string
	session  *Session
}

// NewFile returns a File for a session already in hand, such as one just
// created by logging in.
func NewFile(path, password string, s *Session) *File {
	return &File{path: path, password: password, session: s}
}

// OpenFile reads a stored session.
func OpenFile(path, password string) (*File, error) {
	s, err := Load(path, password)
	if err != nil {
		return nil, err
	}

	return NewFile(path, password, s), nil
}

// Session returns the session being tracked.
func (f *File) Session() *Session {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.session
}

// Save writes the tracked session out as it stands.
func (f *File) Save() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	return Save(f.path, f.password, f.session)
}

// Update records rotated credentials and writes them out.
//
// Called from request goroutines, so it takes the lock; and it writes
// immediately, because the window between rotating and saving is exactly
// where the old token stops working.
func (f *File) Update(uid, refreshToken string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.session.UID == uid && f.session.RefreshToken == refreshToken {
		return nil
	}

	f.session.UID, f.session.RefreshToken = uid, refreshToken

	return Save(f.path, f.password, f.session)
}
