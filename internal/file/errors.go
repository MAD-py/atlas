package file

import "errors"

// Sentinels for internal/file only: this package cannot import the root
// atlas package (atlas will import internal/file, not vice versa), same
// reasoning as internal/storage/errors.go.
var (
	ErrLocked            = errors.New("[Atlas] database file is already open by another connection")
	ErrEmptyDatabasePath = errors.New("[Atlas] database path must not be empty")
)
