//go:build unix

package file

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func Lock(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return nil
	}
	if err == unix.EWOULDBLOCK {
		return ErrLocked
	}
	return fmt.Errorf("%w: %v", ErrLocked, err)
}

func Unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
