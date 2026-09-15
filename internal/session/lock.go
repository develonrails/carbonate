package session

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrLocked means another carbonate process already holds this session.
var ErrLocked = errors.New("another carbonate process is using this session")

// Lock takes an exclusive lock on a session, so that two processes cannot
// share one.
//
// Proton rotates the refresh token on every use and discards the old one, so
// a second process refreshing would strand the first with a token that no
// longer works — and the failure arrives later, on some unrelated request,
// looking like a server problem.
//
// The lock is held on a file beside the session rather than the session
// itself: saving replaces that file by rename, which would carry the lock off
// with the old inode.
func Lock(path string) (*Guard, error) {
	name := path + ".lock"

	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, filePermMode)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}

		return nil, fmt.Errorf("locking session: %w", err)
	}

	return &Guard{file: file}, nil
}

// Guard holds a session lock until it is released.
type Guard struct {
	file *os.File
}

// Release gives up the lock. The kernel would release it when the process
// exits anyway, which is what keeps a crash from leaving a session locked
// forever.
func (g *Guard) Release() error {
	if g == nil || g.file == nil {
		return nil
	}

	file := g.file
	g.file = nil

	return file.Close()
}
