package storage

// FormatVersion is the current on-disk format version, written by NewHeader
// and checked by DecodeHeader. Bump on an incompatible format change.
const FormatVersion uint32 = 1

// DefaultPageSize is used by NewHeader when no page size is requested.
const DefaultPageSize uint32 = 4096

// MaxPageSize is the largest allowed page size: in-page offsets (the future
// data-page slot layout) are uint16, so a page can't exceed what a uint16
// can address.
const MaxPageSize uint32 = 1<<16 - 1
