package storage

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// MaxCollectionNameLen is the fixed on-disk width of a collection's name field.
const MaxCollectionNameLen = 64

// collectionSlotSize is the fixed on-disk size of one collection slot:
//
//	Offset  Size  Field
//	0       1     flags (bit 0 = tombstone, bit 1 = internal collection)
//	1       64    name, zero-padded
//	65      4     head data-page pointer, 0 = none yet (uint32)
//	69      4     tail data-page pointer, 0 = none yet (uint32)
//	73      4     page count (uint32)
//	77      4     doc count (uint32)
//	81      4     checksum, CRC32 over bytes [0:81)
const collectionSlotSize = 85

const (
	slotFlagTombstone byte = 1 << 0
	slotFlagInternal  byte = 1 << 1
)

// CollectionSlot is one catalog entry. PageCount and DocCount are kept as
// two separate counters rather than one combined "page/doc count" (the
// spec's own phrasing is ambiguous here): DocCount lets a future
// Collection.Count() answer in O(1) instead of a full scan, and PageCount is
// independently useful bookkeeping — collapsing them into one field would
// serve neither use case well.
type CollectionSlot struct {
	Flags     byte
	Name      string
	Head      uint32
	Tail      uint32
	PageCount uint32
	DocCount  uint32
}

func (s CollectionSlot) IsTombstone() bool { return s.Flags&slotFlagTombstone != 0 }
func (s CollectionSlot) IsInternal() bool  { return s.Flags&slotFlagInternal != 0 }

// Encode validates the name (unlike header.Encode, which validates
// nothing): header fields are fixed-width integers that can't lose data by
// encoding, but a name over MaxCollectionNameLen bytes would silently
// truncate mid-copy, corrupting it rather than merely encoding an "invalid"
// value. That's worth catching here rather than only at the caller.
func (s CollectionSlot) Encode() ([]byte, error) {
	if err := validateCollectionName(s.Name); err != nil {
		return nil, err
	}
	buf := make([]byte, collectionSlotSize)
	buf[0] = s.Flags
	copy(buf[1:65], s.Name)
	binary.BigEndian.PutUint32(buf[65:69], s.Head)
	binary.BigEndian.PutUint32(buf[69:73], s.Tail)
	binary.BigEndian.PutUint32(buf[73:77], s.PageCount)
	binary.BigEndian.PutUint32(buf[77:81], s.DocCount)
	checksum := crc32.ChecksumIEEE(buf[0:81])
	binary.BigEndian.PutUint32(buf[81:85], checksum)
	return buf, nil
}

// validateCollectionName checks the constraints the spec pins down: byte
// length <= MaxCollectionNameLen. An embedded NUL byte is also rejected
// since the name field is zero-padded on disk and recovered on decode via
// TrimRight — a NUL inside the name itself would be indistinguishable from
// padding and silently truncate the name on the next read.
func validateCollectionName(name string) error {
	if len(name) > MaxCollectionNameLen {
		return ErrCollectionNameTooLong
	}
	if bytes.IndexByte([]byte(name), 0) >= 0 {
		return ErrInvalidCollectionName
	}
	return nil
}

// DecodeCollectionSlot checksum-validates before trusting any field —
// per-slot, so one corrupted slot doesn't invalidate the rest of the page.
func DecodeCollectionSlot(buf []byte) (CollectionSlot, error) {
	if len(buf) != collectionSlotSize {
		return CollectionSlot{}, ErrShortPageRead
	}
	wantChecksum := binary.BigEndian.Uint32(buf[81:85])
	gotChecksum := crc32.ChecksumIEEE(buf[0:81])
	if wantChecksum != gotChecksum {
		return CollectionSlot{}, ErrCorruptedCollectionSlot
	}
	return CollectionSlot{
		Flags:     buf[0],
		Name:      string(bytes.TrimRight(buf[1:65], "\x00")),
		Head:      binary.BigEndian.Uint32(buf[65:69]),
		Tail:      binary.BigEndian.Uint32(buf[69:73]),
		PageCount: binary.BigEndian.Uint32(buf[73:77]),
		DocCount:  binary.BigEndian.Uint32(buf[77:81]),
	}, nil
}
