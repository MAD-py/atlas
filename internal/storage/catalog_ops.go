package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// SlotRef addresses one collection slot: which catalog page, which index
// within it. Enough for a caller to later update the slot in place (e.g.
// bumping a collection's tail pointer on data-page append).
type SlotRef struct {
	Page  uint32
	Index uint32
}

// CollectionEntry pairs a decoded slot with its address.
type CollectionEntry struct {
	Ref  SlotRef
	Slot CollectionSlot
}

// maxCatalogChainLength bounds how many pages a catalog chain walk may visit
// before it's treated as corrupted. The chain can never legitimately be
// longer than the file's total page count, so exceeding this means a
// corrupted next pointer has formed a cycle — without this bound, every
// chain walk below would loop forever instead of erroring.
func maxCatalogChainLength(h *Header) uint32 {
	return h.PageCount + 1
}

// WriteCollectionSlot overwrites the slot at ref in place, recomputing its
// checksum. Does not touch the page's header (next/occupied) — callers that
// need those updated (create, tombstone) handle that separately.
func WriteCollectionSlot(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, ref SlotRef, slot CollectionSlot) error {
	view, err := readCatalogPage(ctx, f, h, ref.Page)
	if err != nil {
		return err
	}
	if ref.Index >= uint32(view.occupied) {
		return ErrInvalidSlotIndex
	}
	encoded, err := slot.Encode()
	if err != nil {
		return err
	}
	copy(view.slotBytes(ref.Index), encoded)
	return WritePage(ctx, f, h, cycle, ref.Page, view.buf)
}

// FindCollectionSlot scans the whole catalog page chain for a live
// (non-tombstoned) slot named name.
//
// A corrupted slot encountered along the way does not abort the scan by
// itself — an uncorrupted match elsewhere is still found and returned. But
// if the scan finishes without a match while corruption was seen, the
// result is reported as corruption rather than ErrCollectionNotFound: the
// corrupted slot's real name is unknown, so "not found" would be a claim
// this code can't actually stand behind.
func FindCollectionSlot(ctx context.Context, f *os.File, h *Header, name string) (SlotRef, CollectionSlot, error) {
	if err := validateCollectionName(name); err != nil {
		return SlotRef{}, CollectionSlot{}, err
	}
	if h.CatalogHead == 0 {
		return SlotRef{}, CollectionSlot{}, ErrCatalogNotInitialized
	}

	var corrupted error
	pageNum := h.CatalogHead
	for visited := uint32(0); pageNum != 0; visited++ {
		if visited >= maxCatalogChainLength(h) {
			return SlotRef{}, CollectionSlot{}, ErrCorruptedCatalogChain
		}
		view, err := readCatalogPage(ctx, f, h, pageNum)
		if err != nil {
			return SlotRef{}, CollectionSlot{}, err
		}
		for i := range uint32(view.occupied) {
			slot, decErr := DecodeCollectionSlot(view.slotBytes(i))
			if decErr != nil {
				corrupted = fmt.Errorf("%w: page %d slot %d", decErr, pageNum, i)
				continue
			}
			if !slot.IsTombstone() && slot.Name == name {
				return SlotRef{Page: pageNum, Index: i}, slot, nil
			}
		}
		pageNum = view.next
	}
	if corrupted != nil {
		return SlotRef{}, CollectionSlot{}, corrupted
	}
	return SlotRef{}, CollectionSlot{}, ErrCollectionNotFound
}

// ListCollectionSlots returns every live collection across the whole chain.
// A corrupted slot is skipped (it can't be decoded, so it can't be listed)
// but does not stop the rest of the scan; every corruption encountered is
// joined into the returned error alongside whatever entries were still
// readable.
func ListCollectionSlots(ctx context.Context, f *os.File, h *Header) ([]CollectionEntry, error) {
	if h.CatalogHead == 0 {
		return nil, ErrCatalogNotInitialized
	}

	var entries []CollectionEntry
	var errs []error
	pageNum := h.CatalogHead
	for visited := uint32(0); pageNum != 0; visited++ {
		if visited >= maxCatalogChainLength(h) {
			return entries, ErrCorruptedCatalogChain
		}
		view, err := readCatalogPage(ctx, f, h, pageNum)
		if err != nil {
			return entries, err
		}
		for i := range uint32(view.occupied) {
			slot, decErr := DecodeCollectionSlot(view.slotBytes(i))
			if decErr != nil {
				errs = append(errs, fmt.Errorf("%w: page %d slot %d", decErr, pageNum, i))
				continue
			}
			if slot.IsTombstone() {
				continue
			}
			entries = append(entries, CollectionEntry{Ref: SlotRef{Page: pageNum, Index: i}, Slot: slot})
		}
		pageNum = view.next
	}
	if len(errs) > 0 {
		return entries, errors.Join(errs...)
	}
	return entries, nil
}

// CreateCollectionSlot scans the entire chain for a name collision among
// live slots, then places the new slot: reuse a tombstoned slot anywhere in
// the chain if one was seen, else append to the last page if it has room,
// else grow the chain with a new page.
func CreateCollectionSlot(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, name string, internal bool) (SlotRef, error) {
	if err := validateCollectionName(name); err != nil {
		return SlotRef{}, err
	}
	if h.CatalogHead == 0 {
		return SlotRef{}, ErrCatalogNotInitialized
	}

	var reuse *SlotRef
	var lastPage uint32
	var lastView *catalogPageView
	pageNum := h.CatalogHead
	for visited := uint32(0); pageNum != 0; visited++ {
		if visited >= maxCatalogChainLength(h) {
			return SlotRef{}, ErrCorruptedCatalogChain
		}
		view, err := readCatalogPage(ctx, f, h, pageNum)
		if err != nil {
			return SlotRef{}, err
		}
		for i := range uint32(view.occupied) {
			slot, decErr := DecodeCollectionSlot(view.slotBytes(i))
			if decErr != nil {
				return SlotRef{}, fmt.Errorf("%w: page %d slot %d", decErr, pageNum, i)
			}
			if slot.IsTombstone() {
				if reuse == nil {
					reuse = &SlotRef{Page: pageNum, Index: i}
				}
				continue
			}
			if slot.Name == name {
				return SlotRef{}, fmt.Errorf("%w: %q", ErrCollectionAlreadyExists, name)
			}
		}
		lastPage, lastView = pageNum, view
		pageNum = view.next
	}

	newSlot := CollectionSlot{Name: name}
	if internal {
		newSlot.Flags |= slotFlagInternal
	}
	encoded, err := newSlot.Encode()
	if err != nil {
		return SlotRef{}, err
	}

	if reuse != nil {
		return *reuse, writeSlotBytes(ctx, f, h, cycle, *reuse, encoded)
	}

	if uint32(lastView.occupied) < lastView.capacity {
		idx := uint32(lastView.occupied)
		copy(lastView.slotBytes(idx), encoded)
		encodeCatalogPageHeader(lastView.buf, lastView.next, uint16(idx+1))
		if err := WritePage(ctx, f, h, cycle, lastPage, lastView.buf); err != nil {
			return SlotRef{}, err
		}
		return SlotRef{Page: lastPage, Index: idx}, nil
	}

	newPageNum, err := Allocate(ctx, f, h, cycle)
	if err != nil {
		return SlotRef{}, err
	}
	newBuf := make([]byte, h.PageSize)
	encodeCatalogPageHeader(newBuf, 0, 1)
	copy(newBuf[catalogPageHeaderSize:catalogPageHeaderSize+collectionSlotSize], encoded)
	if err := WritePage(ctx, f, h, cycle, newPageNum, newBuf); err != nil {
		return SlotRef{}, err
	}

	// Link the new page in only after its content is fully written, so a
	// crash between these two writes leaves an orphaned-but-harmless page
	// (a leak, not a dangling/half-formed pointer a reader could follow) —
	// same reasoning as the rest of this package deferring true atomicity
	// to the future rollback journal.
	encodeCatalogPageHeader(lastView.buf, newPageNum, lastView.occupied)
	if err := WritePage(ctx, f, h, cycle, lastPage, lastView.buf); err != nil {
		return SlotRef{}, err
	}

	return SlotRef{Page: newPageNum, Index: 0}, nil
}

func writeSlotBytes(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, ref SlotRef, encoded []byte) error {
	view, err := readCatalogPage(ctx, f, h, ref.Page)
	if err != nil {
		return err
	}
	copy(view.slotBytes(ref.Index), encoded)
	return WritePage(ctx, f, h, cycle, ref.Page, view.buf)
}

// RemoveCollectionSlot tombstones the named collection's catalog slot.
//
// This is deliberately NOT the spec's full DropCollection: it only frees
// catalog-level bookkeeping. A future root-level DropCollection must first
// walk and free the collection's own data-page chain (head..tail, not
// touched here — data pages don't exist as a module yet), and only then
// call this to reclaim the catalog slot. Calling this alone leaks the
// collection's data pages.
func RemoveCollectionSlot(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, name string) error {
	ref, slot, err := FindCollectionSlot(ctx, f, h, name)
	if err != nil {
		return err
	}

	view, err := readCatalogPage(ctx, f, h, ref.Page)
	if err != nil {
		return err
	}
	slot.Flags |= slotFlagTombstone
	encoded, err := slot.Encode()
	if err != nil {
		return err
	}
	copy(view.slotBytes(ref.Index), encoded)

	allTombstoned := true
	for i := range uint32(view.occupied) {
		s, decErr := DecodeCollectionSlot(view.slotBytes(i))
		if decErr != nil {
			// Can't prove this one is tombstoned too — conservatively
			// keep the page alive rather than freeing out from under
			// data this code can no longer read.
			allTombstoned = false
			continue
		}
		if !s.IsTombstone() {
			allTombstoned = false
			break
		}
	}

	if err := WritePage(ctx, f, h, cycle, ref.Page, view.buf); err != nil {
		return err
	}

	if allTombstoned && ref.Page != h.CatalogHead {
		return unlinkAndFreeCatalogPage(ctx, f, h, cycle, ref.Page)
	}
	return nil
}

// unlinkAndFreeCatalogPage removes pageNum from the catalog chain and
// returns it to the free-list. The caller must have already verified
// pageNum is not the anchor page (h.CatalogHead) — the anchor is never
// freed even when empty, so the bootstrap invariant "header always points
// to a valid catalog page" never needs a special case.
func unlinkAndFreeCatalogPage(ctx context.Context, f *os.File, h *Header, cycle *JournalCycle, pageNum uint32) error {
	predPageNum := h.CatalogHead
	for visited := uint32(0); predPageNum != 0; visited++ {
		if visited >= maxCatalogChainLength(h) {
			return ErrCorruptedCatalogChain
		}
		predView, err := readCatalogPage(ctx, f, h, predPageNum)
		if err != nil {
			return err
		}
		if predView.next == pageNum {
			target, err := readCatalogPage(ctx, f, h, pageNum)
			if err != nil {
				return err
			}
			// Unlink before freeing: a page must never be simultaneously
			// reachable via the catalog chain and the free-list.
			encodeCatalogPageHeader(predView.buf, target.next, predView.occupied)
			if err := WritePage(ctx, f, h, cycle, predPageNum, predView.buf); err != nil {
				return err
			}
			return Free(ctx, f, h, cycle, pageNum)
		}
		predPageNum = predView.next
	}
	return ErrInvalidPageNumber
}
