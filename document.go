package atlas

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

const AtlasIDSize = 12

// AtlasID: 4 bytes Unix-seconds timestamp + 4 bytes random + 4 bytes counter,
// all big-endian so raw byte comparison already sorts chronologically.
type AtlasID [AtlasIDSize]byte

// idCounter resets on every process restart; cross-restart uniqueness relies on the random bytes, not this counter.
var idCounter uint32

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

type Document struct {
	ID     AtlasID
	Fields map[string]any
}
