package storage

import (
	"context"
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

// pageExistsOnDisk reports whether pageNum's full page already lies within
// f's current physical extent. A page beyond it (Allocate's grow-file path)
// has nothing to snapshot — gap 1 from the design review.
func pageExistsOnDisk(f *os.File, pageSize, pageNum uint32) (bool, error) {
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	end := uint64(pageNum)*uint64(pageSize) + uint64(pageSize)
	if end > math.MaxInt64 {
		return false, ErrPageOffsetOverflow
	}
	return info.Size() >= int64(end), nil
}

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
// Page 0 is the header page. Never journaled — reads have nothing to
// protect.
func ReadPage(ctx context.Context, f *os.File, pageSize, pageNum uint32) ([]byte, error) {
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

// writePageRaw is the actual on-disk write, no journaling — what every
// journaled write funnels through once snapshot/dirty housekeeping is done,
// and what journal replay itself uses to restore original page content.
func writePageRaw(f *os.File, pageSize, pageNum uint32, data []byte) error {
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

// WritePage is the single choke point every higher-level page write funnels
// through — where a page about to be modified is snapshotted into the
// active journal cycle on its first touch, uniformly across page kinds
// including the header (page 0). Writing beyond the current end of the file
// grows it.
func WritePage(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, pageNum uint32, data []byte) error {
	if h.PageSize < MinPageSize || h.PageSize > MaxPageSize {
		return ErrInvalidPageSize
	}
	if uint32(len(data)) != h.PageSize {
		return fmt.Errorf("%w: got %d bytes, want %d", ErrInvalidPageData, len(data), h.PageSize)
	}
	if err := cycle.snapshotIfNeeded(ctx, f, h, pageNum); err != nil {
		return err
	}
	if err := cycle.ensureStarted(ctx, f, h); err != nil {
		return err
	}
	return writePageRaw(f, h.PageSize, pageNum, data)
}

// ReadHeader reads just the header (HeaderSize bytes at offset 0) without
// going through ReadPage, since ReadPage needs pageSize — the one thing not
// yet known on a fresh Open() bootstrap.
func ReadHeader(ctx context.Context, f *os.File) (*Header, error) {
	buf := make([]byte, HeaderSize)
	n, err := f.ReadAt(buf, 0)
	if err != nil || n != HeaderSize {
		return nil, fmt.Errorf("%w: %v", ErrTruncatedHeader, err)
	}
	return DecodeHeader(buf)
}

// WriteHeader writes h as page 0, packing h's HeaderSize-byte encoding into
// a full h.PageSize-byte page. It calls ensureStarted itself, before
// encoding h, so a cycle's first-write dirty-forcing (which mutates
// h.Dirty) is reflected in what actually gets encoded — encoding first and
// forcing after would silently persist a stale dirty bit.
func WriteHeader(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle) error {
	if err := cycle.ensureStarted(ctx, f, h); err != nil {
		return err
	}
	buf := make([]byte, h.PageSize)
	copy(buf, h.Encode())
	return WritePage(ctx, f, h, cycle, 0, buf)
}
