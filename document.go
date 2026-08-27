package atlas

import (
	"crypto/rand"
	"encoding/binary"
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

type Document struct {
	ID     AtlasID
	Fields map[string]any
}
