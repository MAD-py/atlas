//go:build windows

package file

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Locking the max range, not just byte 0, mirrors flock's whole-file scope
// even as the .db file grows past whatever size it was at lock time.
const (
	lockRangeLow  = 0xFFFFFFFF
	lockRangeHigh = 0xFFFFFFFF
)

func Lock(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, lockRangeLow, lockRangeHigh,
		&overlapped,
	)
	if err == nil {
		return nil
	}
	if err == windows.ERROR_LOCK_VIOLATION {
		return ErrLocked
	}
	return fmt.Errorf("%w: %v", ErrLocked, err)
}

func Unlock(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRangeLow, lockRangeHigh, &overlapped)
}
