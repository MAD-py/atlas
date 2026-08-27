package encoding

import "errors"

// Sentinels for internal/encoding only: internal/encoding cannot import the
// root atlas package (atlas will import internal/encoding, not vice versa),
// so the codec keeps its own errors instead of reusing atlas/errors.go.
var (
	ErrUnknownTag      = errors.New("[Atlas] unknown type tag encountered during decode")
	ErrInvalidUTF8     = errors.New("[Atlas] invalid UTF-8 in encoded string")
	ErrCorruptedData   = errors.New("[Atlas] corrupted TLV data")
	ErrTruncatedInput  = errors.New("[Atlas] truncated data during decode")
	ErrUnsupportedType = errors.New("[Atlas] unsupported Go type for encoding")
)
