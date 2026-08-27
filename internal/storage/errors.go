package storage

import "errors"

// Sentinels for internal/storage only: this package cannot import the root
// atlas package (atlas will import internal/storage, not vice versa), same
// reasoning as internal/encoding/errors.go. Wiring these into root
// errors.go is a later task's job.
var (
	ErrNotAnAtlasFile      = errors.New("[Atlas] not a valid atlas database file")
	ErrCorruptedHeader     = errors.New("[Atlas] file header checksum mismatch")
	ErrTruncatedHeader     = errors.New("[Atlas] file is too short to contain a valid header")
	ErrIncompatibleVersion = errors.New("[Atlas] incompatible file format version")

	ErrShortPageRead      = errors.New("[Atlas] short read: page data truncated")
	ErrInvalidPageData    = errors.New("[Atlas] page data does not match page size")
	ErrInvalidPageSize    = errors.New("[Atlas] invalid page size")
	ErrPageWriteFailed    = errors.New("[Atlas] page write failed")
	ErrInvalidPageNumber  = errors.New("[Atlas] invalid page number")
	ErrPageOffsetOverflow = errors.New("[Atlas] page offset exceeds addressable file size")

	ErrNotAFreePage      = errors.New("[Atlas] page is not a free-list page")
	ErrPageCountOverflow = errors.New("[Atlas] page count exhausted")
)
