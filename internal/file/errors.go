package file

import "errors"

// Sentinels for internal/file only: this package cannot import the root
// atlas package (atlas will import internal/file, not vice versa), same
// reasoning as internal/storage/errors.go. Plain, unprefixed messages —
// atlas.Error is what actually reaches a caller, and it supplies its own
// namespacing.
var (
	ErrLocked            = errors.New("database file is already open by another connection")
	ErrEmptyDatabasePath = errors.New("database path must not be empty")
)
