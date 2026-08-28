package storage

import (
	"fmt"
	"math"
	"os"
)

// PageType is the leading byte every page kind shares (free-list, catalog,
// and eventually data/journal pages) — the seed of a generic page header.
type PageType byte

const (
	// PageTypeInvalid is the zero value, so a page freshly grown by
	// Allocate (zero-filled, not yet given real content) can't be misread
	// as an already-linked page of some other kind.
	PageTypeInvalid PageType = 0
	PageTypeFree    PageType = 1
	PageTypeCatalog PageType = 2
	PageTypeData    PageType = 3
)

// pageOffset uses uint64 so a corrupted/adversarial (pageNum, pageSize)
// pair that would overflow int64 is caught explicitly instead of wrapping
// to a bogus negative offset.
func pageOffset(pageSize, pageNum uint32) (int64, error) {
	offset := uint64(pageNum) * uint64(pageSize)
	if offset > math.MaxInt64 {
		return 0, ErrPageOffsetOverflow
	}
	return int64(offset), nil
}

// ReadPage reads the raw, exactly-pageSize-byte content of page pageNum.
// Page 0 is the header page.
func ReadPage(f *os.File, pageSize, pageNum uint32) ([]byte, error) {
	if pageSize < MinPageSize || pageSize > MaxPageSize {
		return nil, ErrInvalidPageSize
	}
	offset, err := pageOffset(pageSize, pageNum)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, pageSize)
	n, err := f.ReadAt(buf, offset)
	if err != nil || n != int(pageSize) {
		return nil, fmt.Errorf("%w: page %d: %v", ErrShortPageRead, pageNum, err)
	}
	return buf, nil
}

// WritePage is the single choke point every higher-level page write funnels
// through — the seam a future rollback journal wraps to snapshot a page
// before modifying it, uniformly across page kinds including the header
// (page 0). Writing beyond the current end of the file grows it.
func WritePage(f *os.File, pageSize, pageNum uint32, data []byte) error {
	if pageSize < MinPageSize || pageSize > MaxPageSize {
		return ErrInvalidPageSize
	}
	if uint32(len(data)) != pageSize {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidPageData, len(data), pageSize)
	}
	offset, err := pageOffset(pageSize, pageNum)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(data, offset); err != nil {
		return fmt.Errorf("%w: page %d: %v", ErrPageWriteFailed, pageNum, err)
	}
	return nil
}

// ReadHeader reads just the header (HeaderSize bytes at offset 0) without
// going through ReadPage, since ReadPage needs pageSize — the one thing not
// yet known on a fresh Open() bootstrap.
func ReadHeader(f *os.File) (*Header, error) {
	buf := make([]byte, HeaderSize)
	n, err := f.ReadAt(buf, 0)
	if err != nil || n != HeaderSize {
		return nil, fmt.Errorf("%w: %v", ErrTruncatedHeader, err)
	}
	return DecodeHeader(buf)
}

// WriteHeader writes h as page 0 through WritePage, packing h's
// HeaderSize-byte encoding into a full h.PageSize-byte page.
func WriteHeader(f *os.File, h *Header) error {
	buf := make([]byte, h.PageSize)
	copy(buf, h.Encode())
	return WritePage(f, h.PageSize, 0, buf)
}
