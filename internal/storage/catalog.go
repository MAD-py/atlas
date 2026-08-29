package storage

import (
	"context"
	"encoding/binary"
	"os"
)

// catalogPageHeaderSize is the fixed prefix of every catalog page:
//
//	Offset  Size  Field
//	0       1     page_type (PageTypeCatalog)
//	1       4     next catalog page number, 0 = end of chain (uint32)
//	5       2     occupied slot count (uint16)
//
// No page_checksum (unlike the future data-page header) — the spec's
// checksum granularity for the catalog is per-slot, not per-page. No
// free_start_offset either: slots are fixed-size and packed contiguously
// from the start of the array, so nothing needs to track where free space
// begins.
const catalogPageHeaderSize = 7

// occupiedSlotCount: indices [0, occupiedSlotCount) hold real slot records
// (live or tombstoned); beyond that is unwritten. Only CreateCollectionSlot's
// reuse path repopulates a tombstoned index, so this never shrinks on its own.
func encodeCatalogPageHeader(buf []byte, next uint32, occupiedSlotCount uint16) {
	buf[0] = byte(PageTypeCatalog)
	binary.BigEndian.PutUint32(buf[1:5], next)
	binary.BigEndian.PutUint16(buf[5:7], occupiedSlotCount)
}

func decodeCatalogPageHeader(buf []byte) (next uint32, occupiedSlotCount uint16, err error) {
	if len(buf) < catalogPageHeaderSize {
		return 0, 0, ErrShortPageRead
	}
	if PageType(buf[0]) != PageTypeCatalog {
		return 0, 0, ErrNotACatalogPage
	}
	next = binary.BigEndian.Uint32(buf[1:5])
	occupiedSlotCount = binary.BigEndian.Uint16(buf[5:7])
	return next, occupiedSlotCount, nil
}

// catalogCapacity computes slots-per-page from pageSize at runtime — never
// hardcoded, since page size is configurable per file. A page too small to
// hold even one slot is rejected rather than silently returning 0.
func catalogCapacity(pageSize uint32) (uint32, error) {
	if pageSize <= catalogPageHeaderSize {
		return 0, ErrCatalogPageTooSmall
	}
	capacity := (pageSize - catalogPageHeaderSize) / collectionSlotSize
	if capacity == 0 {
		return 0, ErrCatalogPageTooSmall
	}
	return capacity, nil
}

// catalogPageView is a decoded catalog page plus its backing buffer, so
// callers can address slot byte ranges without re-reading.
type catalogPageView struct {
	next     uint32
	occupied uint16
	capacity uint32
	buf      []byte
}

// readCatalogPage validates occupied <= capacity before returning — without
// this, a corrupted on-disk occupied count could drive slot-index arithmetic
// past the end of buf (same overflow-bypasses-bounds-check shape flagged in
// internal/encoding's post-review; see agent-notes).
func readCatalogPage(ctx context.Context, f *os.File, h *Header, pageNum uint32) (*catalogPageView, error) {
	buf, err := ReadPage(ctx, f, h.PageSize, pageNum)
	if err != nil {
		return nil, err
	}
	next, occupied, err := decodeCatalogPageHeader(buf)
	if err != nil {
		return nil, err
	}
	capacity, err := catalogCapacity(h.PageSize)
	if err != nil {
		return nil, err
	}
	if uint32(occupied) > capacity {
		return nil, ErrCorruptedCatalogPage
	}
	return &catalogPageView{
		next:     next,
		occupied: occupied,
		capacity: capacity,
		buf:      buf,
	}, nil
}

// slotBytes is safe against out-of-bounds access: i is always bounded by
// v.occupied, which readCatalogPage already validated against v.capacity.
func (v *catalogPageView) slotBytes(i uint32) []byte {
	off := catalogPageHeaderSize + i*collectionSlotSize
	return v.buf[off : off+collectionSlotSize]
}

func writeBlankCatalogPage(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, pageNum uint32) error {
	buf := make([]byte, h.PageSize)
	encodeCatalogPageHeader(buf, 0, 0)
	return WritePage(ctx, f, h, cycle, pageNum, buf)
}

// Bootstrap formats a brand-new .db file: header, then the anchor catalog
// page (page 1 on a fresh file, via the existing Allocate), CatalogHead
// pointed at it. No file lock — future work.
//
// Unlike every other mutating function in this package, Bootstrap does not
// take a *JournalCycle parameter: it's the one operation for which no
// *Header yet exists for a caller to construct a cycle from, since
// Bootstrap is what creates that header. It opens and commits its own cycle
// internally instead — a brand-new file has no prior state to protect, but
// there's no separate "unprotected write" path left in this package, so it
// still funnels through the same mechanism.
func Bootstrap(ctx context.Context, f *os.File, pageSize uint32) (*Header, error) {
	h, err := NewHeader(pageSize)
	if err != nil {
		return nil, err
	}
	if _, err := catalogCapacity(h.PageSize); err != nil {
		return nil, err
	}

	cycle, err := NewJournalCycle(ctx, f, h)
	if err != nil {
		return nil, err
	}

	if err := WriteHeader(ctx, f, h, cycle); err != nil {
		_ = cycle.Abort(ctx, f, h)
		return nil, err
	}

	anchor, err := Allocate(ctx, f, h, cycle)
	if err != nil {
		_ = cycle.Abort(ctx, f, h)
		return nil, err
	}
	if err := writeBlankCatalogPage(ctx, f, h, cycle, anchor); err != nil {
		_ = cycle.Abort(ctx, f, h)
		return nil, err
	}

	h.CatalogHead = anchor
	if err := WriteHeader(ctx, f, h, cycle); err != nil {
		_ = cycle.Abort(ctx, f, h)
		return nil, err
	}

	if err := cycle.Commit(ctx, f, h); err != nil {
		return nil, err
	}
	return h, nil
}
