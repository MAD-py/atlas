package atlas

import (
	"errors"
	"fmt"

	"github.com/MAD-py/atlas/internal/file"
	"github.com/MAD-py/atlas/internal/storage"
)

// Sentinel errors, ordered by ascending identifier length. Each is the fixed
// base message; wrapInternalErr adds the dynamic context at the call site.
var (
	ErrClosed = errors.New("[Atlas] database is closed")
	ErrLocked = errors.New("[Atlas] database file is already open by another connection")

	ErrCorruptedPage = errors.New("[Atlas] page checksum mismatch")

	ErrInvalidAtlasID = errors.New("[Atlas] invalid AtlasID string")
	ErrJournalMissing = errors.New("[Atlas] dirty shutdown detected but no valid journal to recover")
	ErrNotAnAtlasFile = errors.New("[Atlas] not a valid atlas database file")

	ErrCorruptedHeader = errors.New("[Atlas] file header checksum mismatch")

	ErrDocumentNotFound = errors.New("[Atlas] document not found")
	ErrDocumentTooLarge = errors.New("[Atlas] document exceeds maximum size")

	ErrCorruptedDocument = errors.New("[Atlas] document checksum mismatch")
	ErrEmptyDatabasePath = errors.New("[Atlas] database path must not be empty")

	ErrCollectionNotFound = errors.New("[Atlas] collection not found")

	ErrIncompatibleVersion = errors.New("[Atlas] incompatible file format version")

	ErrCollectionNameTooLong = errors.New("[Atlas] collection name exceeds maximum length")

	ErrCollectionAlreadyExists = errors.New("[Atlas] collection already exists")
)

// wrapInternalErr maps an internal/storage or internal/file sentinel to its
// root equivalent, so errors.Is(result, rootSentinel) holds for callers of
// the public API. detail is the dynamic context to append (a collection
// name, a file path); pass "" if none is available at the call site.
//
// The underlying internal error's own message is deliberately dropped rather
// than embedded: those packages and root sentinels share identical base
// wording by design, so including both would repeat the same sentence twice.
// This does mean any *extra* detail attached beyond the bare sentinel (e.g.
// ErrJournalMissing's inner replay-failure reason) is lost here — an
// accepted gap, not different in kind from ErrIncompatibleVersion below
// never getting its ideal version-number detail either.
func wrapInternalErr(err error, detail string) error {
	var sentinel error
	switch {
	case errors.Is(err, file.ErrLocked):
		sentinel = ErrLocked
	case errors.Is(err, storage.ErrCorruptedPage):
		sentinel = ErrCorruptedPage
	case errors.Is(err, storage.ErrJournalMissing):
		sentinel = ErrJournalMissing
	case errors.Is(err, storage.ErrNotAnAtlasFile):
		sentinel = ErrNotAnAtlasFile
	case errors.Is(err, storage.ErrCorruptedHeader):
		sentinel = ErrCorruptedHeader
	case errors.Is(err, storage.ErrDocumentNotFound):
		sentinel = ErrDocumentNotFound
	case errors.Is(err, storage.ErrDocumentTooLarge):
		sentinel = ErrDocumentTooLarge
	case errors.Is(err, storage.ErrCorruptedDocument):
		sentinel = ErrCorruptedDocument
	case errors.Is(err, file.ErrEmptyDatabasePath):
		sentinel = ErrEmptyDatabasePath
	case errors.Is(err, storage.ErrCollectionNotFound):
		sentinel = ErrCollectionNotFound
	case errors.Is(err, storage.ErrIncompatibleVersion):
		sentinel = ErrIncompatibleVersion
	case errors.Is(err, storage.ErrCollectionNameTooLong):
		sentinel = ErrCollectionNameTooLong
	case errors.Is(err, storage.ErrCollectionAlreadyExists):
		sentinel = ErrCollectionAlreadyExists
	default:
		return err
	}
	if detail == "" {
		return sentinel
	}
	return fmt.Errorf("%w: %s", sentinel, detail)
}
