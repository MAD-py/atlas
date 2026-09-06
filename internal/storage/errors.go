package storage

import "errors"

// Sentinels for internal/storage only: this package cannot import the root
// atlas package (atlas will import internal/storage, not vice versa), same
// reasoning as internal/encoding/errors.go. Plain, unprefixed messages —
// atlas.Error is what actually reaches a caller, and it supplies its own
// namespacing.
var (
	ErrNotAnAtlasFile      = errors.New("not a valid atlas database file")
	ErrCorruptedHeader     = errors.New("file header checksum mismatch")
	ErrTruncatedHeader     = errors.New("file is too short to contain a valid header")
	ErrIncompatibleVersion = errors.New("incompatible file format version")

	ErrShortPageRead      = errors.New("short read: page data truncated")
	ErrInvalidPageData    = errors.New("page data does not match page size")
	ErrInvalidPageSize    = errors.New("invalid page size")
	ErrPageWriteFailed    = errors.New("page write failed")
	ErrInvalidPageNumber  = errors.New("invalid page number")
	ErrPageOffsetOverflow = errors.New("page offset exceeds addressable file size")

	ErrNotAFreePage      = errors.New("page is not a free-list page")
	ErrPageCountOverflow = errors.New("page count exhausted")

	ErrNotACatalogPage         = errors.New("page is not a catalog page")
	ErrInvalidSlotIndex        = errors.New("slot index out of range for this catalog page")
	ErrCollectionNotFound      = errors.New("collection not found")
	ErrCatalogPageTooSmall     = errors.New("page size too small to hold a single catalog slot")
	ErrCorruptedCatalogPage    = errors.New("catalog page slot count exceeds page capacity")
	ErrCollectionNameTooLong   = errors.New("collection name exceeds maximum length")
	ErrInvalidCollectionName   = errors.New("collection name is invalid")
	ErrCatalogNotInitialized   = errors.New("catalog has not been bootstrapped")
	ErrCorruptedCatalogChain   = errors.New("catalog page chain exceeds the file's page count")
	ErrCollectionAlreadyExists = errors.New("collection already exists")
	ErrCorruptedCollectionSlot = errors.New("collection slot checksum mismatch")

	ErrNotADataPage       = errors.New("page is not a data page")
	ErrDataPageFull       = errors.New("data page has no room for this record")
	ErrCorruptedPage      = errors.New("page checksum mismatch")
	ErrDocumentNotFound   = errors.New("document not found")
	ErrDocumentTooLarge   = errors.New("document exceeds maximum size")
	ErrCorruptedDataPage  = errors.New("data page header or slot bounds are invalid")
	ErrCorruptedDocument  = errors.New("document checksum mismatch")
	ErrCorruptedDataChain = errors.New("data page chain exceeds the file's page count")

	ErrJournalMissing         = errors.New("dirty shutdown detected but no valid journal to recover")
	ErrNotAJournalFile        = errors.New("not a valid atlas journal file")
	ErrJournalOpenFailed      = errors.New("failed to open journal file")
	ErrJournalCycleClosed     = errors.New("journal cycle already committed or aborted")
	ErrJournalWriteFailed     = errors.New("journal write failed")
	ErrShortJournalRecord     = errors.New("short read: journal record truncated")
	ErrCorruptedJournalHeader = errors.New("journal header checksum mismatch")
	ErrCorruptedJournalRecord = errors.New("journal record checksum mismatch")
	ErrTruncatedJournalHeader = errors.New("journal file is too short to contain a valid header")
)
