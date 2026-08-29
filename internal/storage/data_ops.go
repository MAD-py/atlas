package storage

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"os"
)

// maxDataChainLength bounds a collection's data-page chain walk, same
// reasoning as maxCatalogChainLength in catalog_ops.go: the chain can never
// legitimately be longer than the file's total page count, so exceeding
// this means a corrupted next pointer formed a cycle.
func maxDataChainLength(h *Header) uint32 {
	return h.PageCount + 1
}

// InsertRecord appends record (id + TLV body, already concatenated by the
// caller — this package never decodes it) to the collection identified by
// catalogRef/slot. It always writes at the tail: (a) the tail page's
// contiguous room if it fits, (b) compacted room if that would fit, (c) a
// newly allocated page linked in as the new tail otherwise. slot is mutated
// in place (Head/Tail/PageCount/DocCount) and persisted via
// WriteCollectionSlot before returning.
func InsertRecord(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, catalogRef SlotRef, slot *CollectionSlot, record []byte) (pageNum, slotIndex uint32, err error) {
	maxSize := dataPageMaxRecordSize(h.PageSize)
	if uint32(len(record)) > maxSize {
		return 0, 0, fmt.Errorf("%w: %d bytes, blank-page limit is %d bytes", ErrDocumentTooLarge, len(record), maxSize)
	}
	recLen := uint32(len(record))

	oldTail := slot.Tail
	var view *dataPageView
	var target uint32

	if oldTail != 0 {
		view, err = readDataPage(ctx, f, h, oldTail)
		if err != nil {
			return 0, 0, err
		}
		target = oldTail

		if !view.contiguousRoomFor(recLen) {
			fits, cerr := view.roomAfterCompactionFor(recLen)
			if cerr != nil {
				return 0, 0, cerr
			}
			if fits {
				if err = compactDataPage(view); err != nil {
					return 0, 0, err
				}
			} else {
				view = nil // doesn't fit even after compaction — fall through to a new page
			}
		}
	}

	allocatedNew := view == nil
	if allocatedNew {
		newPageNum, aerr := Allocate(ctx, f, h, cycle)
		if aerr != nil {
			return 0, 0, aerr
		}
		target = newPageNum
		view, err = newBlankDataPage(h.PageSize)
		if err != nil {
			return 0, 0, err
		}
	}

	idx, werr := writeRecordIntoPage(view, record)
	if werr != nil {
		return 0, 0, werr
	}
	if err = WritePage(ctx, f, h, cycle, target, view.buf); err != nil {
		return 0, 0, err
	}

	// Link the new tail in only after its content is fully written — same
	// "content first, link second" ordering as CreateCollectionSlot's chain
	// growth (catalog_ops.go): a crash between these two writes leaves an
	// orphaned-but-harmless page, never a dangling pointer a reader could
	// follow into a still-blank page.
	if allocatedNew && oldTail != 0 {
		if err = linkNextDataPage(ctx, f, h, cycle, oldTail, target); err != nil {
			return 0, 0, err
		}
	}

	if slot.Head == 0 {
		slot.Head = target
	}
	slot.Tail = target
	if allocatedNew {
		slot.PageCount++
	}
	slot.DocCount++
	if err = WriteCollectionSlot(ctx, f, h, cycle, catalogRef, *slot); err != nil {
		return 0, 0, err
	}

	return target, idx, nil
}

func linkNextDataPage(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, pageNum, next uint32) error {
	view, err := readDataPage(ctx, f, h, pageNum)
	if err != nil {
		return err
	}
	view.setHeader(next, view.freeStart, view.slotCount)
	if err := view.finalize(); err != nil {
		return err
	}
	return WritePage(ctx, f, h, cycle, pageNum, view.buf)
}

// FindRecordByID walks the collection's page chain from head, comparing
// each live record's leading recordIDSize bytes directly against id — no
// TLV decoding needed, since a record's id always lives at a fixed offset
// regardless of its encoded fields. A slot whose offset/length can't be
// trusted (readDataPage already bounds-checked the page itself, but a
// single slot's own fields could still be corrupted) is skipped rather than
// aborting the scan, since it might not even be the record being searched
// for. Once an id match is found, its own per-record checksum is verified
// before the bytes are handed back — a corrupted OTHER record earlier in
// the same page never blocks reaching this one.
func FindRecordByID(ctx context.Context, f *os.File, h *Header, head uint32, id [recordIDSize]byte) (record []byte, pageNum, slotIndex uint32, err error) {
	pageNum = head
	for visited := uint32(0); pageNum != 0; visited++ {
		if visited >= maxDataChainLength(h) {
			return nil, 0, 0, ErrCorruptedDataChain
		}
		view, verr := readDataPage(ctx, f, h, pageNum)
		if verr != nil {
			return nil, 0, 0, verr
		}

		for i := range uint32(view.slotCount) {
			slot, serr := view.slotAt(i)
			if serr != nil {
				return nil, 0, 0, serr
			}
			if slot.isTombstone() {
				continue
			}
			recBytes, rerr := view.recordBytes(slot)
			if rerr != nil {
				continue // can't safely read this slot; it can't be proven to be our target either
			}
			if len(recBytes) < recordIDSize || !bytes.Equal(recBytes[:recordIDSize], id[:]) {
				continue
			}
			if crc32.ChecksumIEEE(recBytes) != slot.checksum {
				return nil, 0, 0, fmt.Errorf("%w: id %x at page %d slot %d", ErrCorruptedDocument, id, pageNum, i)
			}
			return recBytes, pageNum, i, nil
		}
		pageNum = view.next
	}
	return nil, 0, 0, ErrDocumentNotFound
}

// DeleteRecord tombstones the record matching id, tracking the previous
// page as it scans so an emptied page can be unlinked using the predecessor
// already in hand — no re-walk from head the way
// unlinkAndFreeCatalogPage (catalog_ops.go) needs to, since that function
// isn't handed a predecessor by its caller and this one is.
func DeleteRecord(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, catalogRef SlotRef, slot *CollectionSlot, id [recordIDSize]byte) error {
	if slot.Head == 0 {
		return ErrDocumentNotFound
	}

	var prevPage uint32
	pageNum := slot.Head
	for visited := uint32(0); pageNum != 0; visited++ {
		if visited >= maxDataChainLength(h) {
			return ErrCorruptedDataChain
		}
		view, err := readDataPage(ctx, f, h, pageNum)
		if err != nil {
			return err
		}

		found := false
		for i := range uint32(view.slotCount) {
			off, ok := dataSlotOffset(h.PageSize, i)
			if !ok {
				return ErrCorruptedDataPage
			}
			s := decodeDataSlot(view.buf[off : off+dataSlotSize])
			if s.isTombstone() {
				continue
			}
			recBytes, rerr := view.recordBytes(s)
			if rerr != nil {
				continue
			}
			if len(recBytes) < recordIDSize || !bytes.Equal(recBytes[:recordIDSize], id[:]) {
				continue
			}
			s.flags |= dataSlotFlagTombstone
			encodeDataSlot(view.buf[off:off+dataSlotSize], s)
			found = true
			break
		}

		if !found {
			prevPage = pageNum
			pageNum = view.next
			continue
		}

		if err := view.finalize(); err != nil {
			return err
		}
		if err := WritePage(ctx, f, h, cycle, pageNum, view.buf); err != nil {
			return err
		}
		slot.DocCount--

		remaining, lerr := view.liveSlotCount()
		if lerr != nil {
			return lerr
		}
		if remaining == 0 {
			if err := freeEmptyDataPage(ctx, f, h, cycle, prevPage, pageNum, view.next, slot); err != nil {
				return err
			}
		}

		return WriteCollectionSlot(ctx, f, h, cycle, catalogRef, *slot)
	}

	return ErrDocumentNotFound
}

// freeEmptyDataPage unlinks pageNum (whose last live record was just
// tombstoned) from the collection's chain and returns it to the free-list
// immediately, rather than deferring reclaim to some later pass.
// prevPage == 0 means
// pageNum was the chain's head. The Head/Tail/PageCount fields on slot are
// updated in memory here; the caller persists them via WriteCollectionSlot.
func freeEmptyDataPage(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, prevPage, pageNum, nextPage uint32, slot *CollectionSlot) error {
	if prevPage == 0 {
		slot.Head = nextPage
	} else {
		prevView, err := readDataPage(ctx, f, h, prevPage)
		if err != nil {
			return err
		}
		prevView.setHeader(nextPage, prevView.freeStart, prevView.slotCount)
		if err := prevView.finalize(); err != nil {
			return err
		}
		// Unlink before freeing: pageNum must never be simultaneously
		// reachable via the collection's chain and the free-list.
		if err := WritePage(ctx, f, h, cycle, prevPage, prevView.buf); err != nil {
			return err
		}
	}

	if slot.Tail == pageNum {
		if prevPage == 0 {
			slot.Tail = nextPage // single-page collection: both become 0
		} else {
			slot.Tail = prevPage
		}
	}

	if slot.PageCount > 0 {
		slot.PageCount--
	}

	return Free(ctx, f, h, cycle, pageNum)
}
