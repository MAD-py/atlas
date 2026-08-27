package storage

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// HeaderSize is the fixed on-disk size of the file header. The header
// occupies page 0 (see page.go): HeaderSize bytes at the front, the rest of
// the page zero-padded.
const HeaderSize = 64

// MinPageSize: a page must be able to hold the header, since page 0 is a
// full page with the header packed into its front.
const MinPageSize = HeaderSize

var magic = [8]byte{'A', 'T', 'L', 'A', 'S', 'D', 'B', 0x00}

// Header is the fixed-offset layout at the start of every .db file:
//
//	Offset  Size  Field
//	0       8     magic bytes ("ATLASDB\0")
//	8       4     format version (uint32)
//	12      4     page size in bytes (uint32, immutable after creation)
//	16      4     total page count, excluding the header page (uint32)
//	20      4     free-list head page number, 0 = empty (uint32)
//	24      4     catalog head page number, 0 = none yet (uint32)
//	28      1     dirty flag (0 = clean, 1 = dirty)
//	29      3     reserved (zero)
//	32      4     header checksum, CRC32 over bytes [0:32)
//	36      28    reserved (zero) — headroom for future fields
//
// Page numbers after the header are 1-based, so 0 doubles as the "no page"
// sentinel for FreeListHead/CatalogHead.
type Header struct {
	Version      uint32
	PageSize     uint32
	PageCount    uint32
	FreeListHead uint32
	CatalogHead  uint32
	Dirty        bool
}

// NewHeader builds the header for a brand-new file. pageSize of 0 selects
// DefaultPageSize.
func NewHeader(pageSize uint32) (*Header, error) {
	if pageSize == 0 {
		pageSize = DefaultPageSize
	}
	if pageSize < MinPageSize || pageSize > MaxPageSize {
		return nil, ErrInvalidPageSize
	}
	return &Header{
		Version:  FormatVersion,
		PageSize: pageSize,
	}, nil
}

// ValidatePageCount checks fileSize against the header page plus
// h.PageCount data pages. uint64 arithmetic avoids overflow on
// corrupted/adversarial PageCount/PageSize values read off disk.
func (h *Header) ValidatePageCount(fileSize int64) error {
	if fileSize < 0 {
		return ErrCorruptedHeader
	}
	want := (uint64(h.PageCount) + 1) * uint64(h.PageSize)
	if want != uint64(fileSize) {
		return ErrCorruptedHeader
	}
	return nil
}

// Encode serializes h, computing a fresh checksum. No validation — that's
// DecodeHeader's job, so an intentionally invalid header can still be encoded.
func (h *Header) Encode() []byte {
	buf := make([]byte, HeaderSize)
	copy(buf[0:8], magic[:])
	binary.BigEndian.PutUint32(buf[8:12], h.Version)
	binary.BigEndian.PutUint32(buf[12:16], h.PageSize)
	binary.BigEndian.PutUint32(buf[16:20], h.PageCount)
	binary.BigEndian.PutUint32(buf[20:24], h.FreeListHead)
	binary.BigEndian.PutUint32(buf[24:28], h.CatalogHead)
	if h.Dirty {
		buf[28] = 1
	}
	checksum := crc32.ChecksumIEEE(buf[0:32])
	binary.BigEndian.PutUint32(buf[32:36], checksum)
	return buf
}

// DecodeHeader checks magic, then checksum, then version/page size — the
// latter two are only trustworthy once the checksum confirms no corruption.
func DecodeHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderSize {
		return nil, ErrTruncatedHeader
	}
	if !bytes.Equal(buf[0:8], magic[:]) {
		return nil, ErrNotAnAtlasFile
	}

	wantChecksum := binary.BigEndian.Uint32(buf[32:36])
	gotChecksum := crc32.ChecksumIEEE(buf[0:32])
	if wantChecksum != gotChecksum {
		return nil, ErrCorruptedHeader
	}

	h := &Header{
		Version:      binary.BigEndian.Uint32(buf[8:12]),
		PageSize:     binary.BigEndian.Uint32(buf[12:16]),
		PageCount:    binary.BigEndian.Uint32(buf[16:20]),
		FreeListHead: binary.BigEndian.Uint32(buf[20:24]),
		CatalogHead:  binary.BigEndian.Uint32(buf[24:28]),
		Dirty:        buf[28] != 0,
	}
	if h.Version != FormatVersion {
		return nil, ErrIncompatibleVersion
	}
	if h.PageSize < MinPageSize || h.PageSize > MaxPageSize {
		return nil, ErrCorruptedHeader
	}
	return h, nil
}
