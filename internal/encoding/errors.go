package encoding

import "errors"

// Sentinels for internal/encoding only: internal/encoding cannot import the
// root atlas package (atlas will import internal/encoding, not vice versa),
// so the codec keeps its own errors instead of reusing atlas/errors.go.
// Plain, unprefixed messages — atlas.Error is what actually reaches a
// caller, and it supplies its own namespacing.
var (
	ErrUnknownTag      = errors.New("unknown type tag encountered during decode")
	ErrInvalidUTF8     = errors.New("invalid UTF-8 in encoded string")
	ErrCorruptedData   = errors.New("corrupted TLV data")
	ErrTruncatedInput  = errors.New("truncated data during decode")
	ErrUnsupportedType = errors.New("unsupported Go type for encoding")
)
