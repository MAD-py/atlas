package atlas

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

// AtlasIDSize is the fixed byte length of an AtlasID.
const AtlasIDSize = 12

// AtlasID is a 12-byte, engine-generated document identifier: a 4-byte
// Unix-seconds timestamp, 4 bytes of randomness, and a 4-byte counter, all
// big-endian so raw byte comparison already sorts ids chronologically.
// Callers never choose an AtlasID's value — Insert always mints its own.
type AtlasID [AtlasIDSize]byte

// idCounter resets on every process restart; cross-restart uniqueness relies on the random bytes, not this counter.
var idCounter uint32

// NewAtlasID generates a new AtlasID from the current time, plus random and
// counter bytes so ids minted within the same second stay distinct.
func NewAtlasID() (AtlasID, error) {
	var id AtlasID
	binary.BigEndian.PutUint32(id[0:4], uint32(time.Now().Unix()))
	if _, err := rand.Read(id[4:8]); err != nil {
		return AtlasID{}, err
	}
	counter := atomic.AddUint32(&idCounter, 1)
	binary.BigEndian.PutUint32(id[8:12], counter)
	return id, nil
}

// String hex-encodes id — the only form of an AtlasID a caller across an API
// boundary (JSON, logs, a URL path segment) should ever need to handle.
func (id AtlasID) String() string {
	return hex.EncodeToString(id[:])
}

// MarshalText makes AtlasID render as a hex string wherever encoding/json
// (or anything else built on encoding.TextMarshaler) serializes it, instead
// of the byte array json.Marshal would otherwise produce.
func (id AtlasID) MarshalText() ([]byte, error) {
	return []byte(id.String()), nil
}

// UnmarshalText is MarshalText's inverse, used by encoding/json and by
// ParseAtlasID.
func (id *AtlasID) UnmarshalText(text []byte) error {
	decoded, err := hex.DecodeString(string(text))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAtlasID, err)
	}
	if len(decoded) != AtlasIDSize {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidAtlasID, len(decoded), AtlasIDSize)
	}
	copy(id[:], decoded)
	return nil
}

// ParseAtlasID decodes an AtlasID.String() value back into an AtlasID.
func ParseAtlasID(s string) (AtlasID, error) {
	var id AtlasID
	if err := id.UnmarshalText([]byte(s)); err != nil {
		return AtlasID{}, err
	}
	return id, nil
}

// Document is a stored record: an id and a schema-less set of fields. The
// same type is used for both writing and reading. On Insert, ID is ignored —
// the engine always mints its own; on read, ID is always populated with the
// document's real id. Fields may hold nil, bool, any signed Go integer type,
// float64, string, time.Time, Date (a distinct, date-only value with no
// time-of-day component), []any, or map[string]any (nested documents); a
// value of any other type makes Insert/Update/Replace fail.
type Document struct {
	ID     AtlasID
	Fields map[string]any
}
