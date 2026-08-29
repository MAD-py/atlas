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

	ErrNotACatalogPage         = errors.New("[Atlas] page is not a catalog page")
	ErrInvalidSlotIndex        = errors.New("[Atlas] slot index out of range for this catalog page")
	ErrCollectionNotFound      = errors.New("[Atlas] collection not found")
	ErrCatalogPageTooSmall     = errors.New("[Atlas] page size too small to hold a single catalog slot")
	ErrCorruptedCatalogPage    = errors.New("[Atlas] catalog page slot count exceeds page capacity")
	ErrCollectionNameTooLong   = errors.New("[Atlas] collection name exceeds maximum length")
	ErrInvalidCollectionName   = errors.New("[Atlas] collection name is invalid")
	ErrCatalogNotInitialized   = errors.New("[Atlas] catalog has not been bootstrapped")
	ErrCorruptedCatalogChain   = errors.New("[Atlas] catalog page chain exceeds the file's page count")
	ErrCollectionAlreadyExists = errors.New("[Atlas] collection already exists")
	ErrCorruptedCollectionSlot = errors.New("[Atlas] collection slot checksum mismatch")

	ErrNotADataPage       = errors.New("[Atlas] page is not a data page")
	ErrDataPageFull       = errors.New("[Atlas] data page has no room for this record")
	ErrCorruptedPage      = errors.New("[Atlas] page checksum mismatch")
	ErrDocumentNotFound   = errors.New("[Atlas] document not found")
	ErrDocumentTooLarge   = errors.New("[Atlas] document exceeds maximum size")
	ErrCorruptedDataPage  = errors.New("[Atlas] data page header or slot bounds are invalid")
	ErrCorruptedDocument  = errors.New("[Atlas] document checksum mismatch")
	ErrCorruptedDataChain = errors.New("[Atlas] data page chain exceeds the file's page count")

	ErrJournalMissing         = errors.New("[Atlas] dirty shutdown detected but no valid journal to recover")
	ErrNotAJournalFile        = errors.New("[Atlas] not a valid atlas journal file")
	ErrJournalOpenFailed      = errors.New("[Atlas] failed to open journal file")
	ErrJournalCycleClosed     = errors.New("[Atlas] journal cycle already committed or aborted")
	ErrJournalWriteFailed     = errors.New("[Atlas] journal write failed")
	ErrShortJournalRecord     = errors.New("[Atlas] short read: journal record truncated")
	ErrCorruptedJournalHeader = errors.New("[Atlas] journal header checksum mismatch")
	ErrCorruptedJournalRecord = errors.New("[Atlas] journal record checksum mismatch")
	ErrTruncatedJournalHeader = errors.New("[Atlas] journal file is too short to contain a valid header")
)
