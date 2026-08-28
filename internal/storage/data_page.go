package storage

import (
	"encoding/binary"
	"hash/crc32"
	"os"
)

// dataPageHeaderSize is the fixed prefix of every data page:
//
//	Offset  Size  Field
//	0       1     page_type (PageTypeData)
//	1       4     next page in this collection's chain, 0 = end (uint32)
//	5       2     free_start_offset (uint16)
//	7       2     slot_count (uint16)
//	9       4     page_checksum (uint32, CRC32)
const dataPageHeaderSize = 13

// dataSlotSize is the fixed size of one entry in the backward-growing slot
// array: flags(1) + offset(uint16) + length(uint16) + checksum(uint32, CRC32).
const dataSlotSize = 9

const dataSlotFlagTombstone byte = 1 << 0

// recordIDSize mirrors internal/encoding.IDSize without importing that
// package — this package must stay opaque to the TLV format, but the fixed
// 12-byte id prefix is a storage-layer fact in its own right (see CLAUDE.md's
// data-page record layout).
const recordIDSize = 12

type dataSlot struct {
	flags    byte
	offset   uint16
	length   uint16
	checksum uint32
}

func (s dataSlot) isTombstone() bool { return s.flags&dataSlotFlagTombstone != 0 }

func encodeDataSlot(buf []byte, s dataSlot) {
	buf[0] = s.flags
	binary.BigEndian.PutUint16(buf[1:3], s.offset)
	binary.BigEndian.PutUint16(buf[3:5], s.length)
	binary.BigEndian.PutUint32(buf[5:9], s.checksum)
}

func decodeDataSlot(buf []byte) dataSlot {
	return dataSlot{
		flags:    buf[0],
		offset:   binary.BigEndian.Uint16(buf[1:3]),
		length:   binary.BigEndian.Uint16(buf[3:5]),
		checksum: binary.BigEndian.Uint32(buf[5:9]),
	}
}

func encodeDataPageHeaderFields(buf []byte, next uint32, freeStart, slotCount uint16) {
	buf[0] = byte(PageTypeData)
	binary.BigEndian.PutUint32(buf[1:5], next)
	binary.BigEndian.PutUint16(buf[5:7], freeStart)
	binary.BigEndian.PutUint16(buf[7:9], slotCount)
}

func decodeDataPageHeaderFields(buf []byte) (next uint32, freeStart, slotCount uint16, checksum uint32, err error) {
	if len(buf) < dataPageHeaderSize {
		return 0, 0, 0, 0, ErrShortPageRead
	}
	if PageType(buf[0]) != PageTypeData {
		return 0, 0, 0, 0, ErrNotADataPage
	}
	next = binary.BigEndian.Uint32(buf[1:5])
	freeStart = binary.BigEndian.Uint16(buf[5:7])
	slotCount = binary.BigEndian.Uint16(buf[7:9])
	checksum = binary.BigEndian.Uint32(buf[9:13])
	return next, freeStart, slotCount, checksum, nil
}

// dataSlotArrayStart returns the byte offset where a slot array of count
// entries begins (grows backward from the end of the page), using uint32
// arithmetic throughout so a corrupted/adversarial count that would
// underflow a direct pageSize-count*dataSlotSize subtraction is caught
// explicitly (ok=false) instead of wrapping to a bogus huge offset.
func dataSlotArrayStart(pageSize, count uint32) (offset uint32, ok bool) {
	need := count * dataSlotSize
	if need > pageSize {
		return 0, false
	}
	return pageSize - need, true
}

// dataSlotOffset locates slot index i: it's exactly where a slot array of
// i+1 entries would begin, since slot 0 sits nearest the end of the page and
// each following index grows the array one slot further back.
func dataSlotOffset(pageSize, index uint32) (offset uint32, ok bool) {
	return dataSlotArrayStart(pageSize, index+1)
}

// dataPageMaxRecordSize is the hard per-document size limit: a blank page's
// usable space (page minus its own header minus one slot entry). Always
// positive for any allowed page size (MinPageSize=64 > 13+9=22).
func dataPageMaxRecordSize(pageSize uint32) uint32 {
	return pageSize - dataPageHeaderSize - dataSlotSize
}

// computeDataPageChecksum covers, in this fixed order: the header fields
// minus the checksum field itself (bytes [0:9)), the whole slot array
// (tombstoned entries included — their metadata is live state, only their
// record bytes are excluded), then every live record's bytes in ascending
// slot-index order. A tombstoned record's underlying bytes are deliberately
// skipped: tombstoning must not change page_checksum, only the flags byte
// (which the slot-array pass already covers) does.
func computeDataPageChecksum(buf []byte, pageSize uint32) (uint32, error) {
	_, freeStart, slotCount, _, err := decodeDataPageHeaderFields(buf)
	if err != nil {
		return 0, err
	}
	slotArrayStart, ok := dataSlotArrayStart(pageSize, uint32(slotCount))
	if !ok || slotArrayStart < dataPageHeaderSize || uint32(freeStart) > slotArrayStart {
		return 0, ErrCorruptedDataPage
	}

	hasher := crc32.NewIEEE()
	hasher.Write(buf[0:9])
	hasher.Write(buf[slotArrayStart:pageSize])

	for i := uint32(0); i < uint32(slotCount); i++ {
		off, ok := dataSlotOffset(pageSize, i)
		if !ok {
			return 0, ErrCorruptedDataPage
		}
		slot := decodeDataSlot(buf[off : off+dataSlotSize])
		if slot.isTombstone() {
			continue
		}
		start := uint32(slot.offset)
		end := start + uint32(slot.length)
		if start < dataPageHeaderSize || end > uint32(freeStart) || end < start {
			return 0, ErrCorruptedDataPage
		}
		hasher.Write(buf[start:end])
	}
	return hasher.Sum32(), nil
}

func storeDataPageChecksum(buf []byte, pageSize uint32) error {
	sum, err := computeDataPageChecksum(buf, pageSize)
	if err != nil {
		return err
	}
	binary.BigEndian.PutUint32(buf[9:13], sum)
	return nil
}

// verifyDataPageChecksum recomputes page_checksum from buf's current
// content and compares it against the stored value.
func verifyDataPageChecksum(buf []byte, pageSize uint32) error {
	_, _, _, want, err := decodeDataPageHeaderFields(buf)
	if err != nil {
		return err
	}
	got, err := computeDataPageChecksum(buf, pageSize)
	if err != nil {
		return err
	}
	if got != want {
		return ErrCorruptedPage
	}
	return nil
}

// dataPageView is a decoded data page plus its backing buffer. The struct's
// next/freeStart/slotCount mirror what's encoded in buf[0:9] — always kept
// in sync via setHeader, never diverging.
type dataPageView struct {
	next      uint32
	freeStart uint16
	slotCount uint16
	buf       []byte
	pageSize  uint32
}

func (v *dataPageView) setHeader(next uint32, freeStart, slotCount uint16) {
	encodeDataPageHeaderFields(v.buf, next, freeStart, slotCount)
	v.next, v.freeStart, v.slotCount = next, freeStart, slotCount
}

// finalize recomputes and stores page_checksum from v.buf's current
// content. Callers must call this after any mutation to v.buf, before the
// page is written to disk.
func (v *dataPageView) finalize() error {
	return storeDataPageChecksum(v.buf, v.pageSize)
}

func (v *dataPageView) slotAt(index uint32) (dataSlot, error) {
	off, ok := dataSlotOffset(v.pageSize, index)
	if !ok {
		return dataSlot{}, ErrCorruptedDataPage
	}
	return decodeDataSlot(v.buf[off : off+dataSlotSize]), nil
}

// recordBytes bounds-checks slot against v.freeStart (the high-water mark
// of everything ever written to this page, live or tombstoned) before
// slicing, so a corrupted offset/length can't drive a panic.
func (v *dataPageView) recordBytes(slot dataSlot) ([]byte, error) {
	start := uint32(slot.offset)
	end := start + uint32(slot.length)
	if start < dataPageHeaderSize || end > uint32(v.freeStart) || end < start {
		return nil, ErrCorruptedDataPage
	}
	return v.buf[start:end], nil
}

func (v *dataPageView) liveBytes() (uint32, error) {
	var total uint32
	for i := uint32(0); i < uint32(v.slotCount); i++ {
		slot, err := v.slotAt(i)
		if err != nil {
			return 0, err
		}
		if !slot.isTombstone() {
			total += uint32(slot.length)
		}
	}
	return total, nil
}

func (v *dataPageView) liveSlotCount() (uint32, error) {
	var n uint32
	for i := uint32(0); i < uint32(v.slotCount); i++ {
		slot, err := v.slotAt(i)
		if err != nil {
			return 0, err
		}
		if !slot.isTombstone() {
			n++
		}
	}
	return n, nil
}

// contiguousRoomFor reports whether recordLen bytes fit in the gap past
// freeStart without compaction, accounting for the new slot entry a fresh
// insert always adds (slotCount+1 — see the "always append" note on
// compactDataPage).
func (v *dataPageView) contiguousRoomFor(recordLen uint32) bool {
	start, ok := dataSlotArrayStart(v.pageSize, uint32(v.slotCount)+1)
	if !ok || start < uint32(v.freeStart) {
		return false
	}
	return start-uint32(v.freeStart) >= recordLen
}

// roomAfterCompactionFor reports whether recordLen bytes would fit once
// tombstoned records' byte space is reclaimed, again accounting for the new
// slot entry the insert will add on top of compaction.
func (v *dataPageView) roomAfterCompactionFor(recordLen uint32) (bool, error) {
	live, err := v.liveBytes()
	if err != nil {
		return false, err
	}
	start, ok := dataSlotArrayStart(v.pageSize, uint32(v.slotCount)+1)
	if !ok {
		return false, nil
	}
	used := uint32(dataPageHeaderSize) + live
	if start < used {
		return false, nil
	}
	return start-used >= recordLen, nil
}

func newBlankDataPage(pageSize uint32) (*dataPageView, error) {
	buf := make([]byte, pageSize)
	view := &dataPageView{buf: buf, pageSize: pageSize}
	view.setHeader(0, dataPageHeaderSize, 0)
	if err := view.finalize(); err != nil {
		return nil, err
	}
	return view, nil
}

// readDataPage decodes structural fields and bounds-checks them (slot array
// fits the page, free_start_offset lies within [header, slotArrayStart]) —
// enough to safely index into the page without panicking. It deliberately
// does NOT gate on page_checksum: that aggregate covers every live record on
// the page, so treating it as a hard read gate would mean one corrupted
// record blocks access to every other document on the page — exactly what
// the per-slot checksum exists to avoid (see agent-notes for the full
// reasoning). Callers that need the aggregate guarantee call
// verifyDataPageChecksum explicitly.
func readDataPage(f *os.File, h *Header, pageNum uint32) (*dataPageView, error) {
	buf, err := ReadPage(f, h.PageSize, pageNum)
	if err != nil {
		return nil, err
	}
	next, freeStart, slotCount, _, err := decodeDataPageHeaderFields(buf)
	if err != nil {
		return nil, err
	}

	slotArrayStart, ok := dataSlotArrayStart(h.PageSize, uint32(slotCount))
	if !ok || slotArrayStart < dataPageHeaderSize {
		return nil, ErrCorruptedDataPage
	}
	if uint32(freeStart) < dataPageHeaderSize || uint32(freeStart) > slotArrayStart {
		return nil, ErrCorruptedDataPage
	}

	return &dataPageView{
		next:      next,
		freeStart: freeStart,
		slotCount: slotCount,
		buf:       buf,
		pageSize:  h.PageSize,
	}, nil
}

// writeRecordIntoPage appends record as a new slot at the current
// freeStart. Callers must have already confirmed room via contiguousRoomFor
// (directly, or via compactDataPage having just made it true) — the bounds
// check here is a defensive backstop, not the primary gate, and returns
// ErrDataPageFull rather than letting a slicing bug panic.
func writeRecordIntoPage(view *dataPageView, record []byte) (uint32, error) {
	newSlotCount := uint32(view.slotCount) + 1
	slotArrayStart, ok := dataSlotArrayStart(view.pageSize, newSlotCount)
	if !ok {
		return 0, ErrDataPageFull
	}
	offset := uint32(view.freeStart)
	end := offset + uint32(len(record))
	if end > slotArrayStart {
		return 0, ErrDataPageFull
	}

	copy(view.buf[offset:end], record)
	checksum := crc32.ChecksumIEEE(view.buf[offset:end])

	slotIndex := uint32(view.slotCount)
	slotOff, ok := dataSlotOffset(view.pageSize, slotIndex)
	if !ok {
		return 0, ErrDataPageFull
	}
	encodeDataSlot(view.buf[slotOff:slotOff+dataSlotSize], dataSlot{
		offset:   uint16(offset),
		length:   uint16(len(record)),
		checksum: checksum,
	})

	view.setHeader(view.next, uint16(end), uint16(newSlotCount))
	if err := view.finalize(); err != nil {
		return 0, err
	}
	return slotIndex, nil
}

// compactDataPage rewrites live records contiguously starting right after
// the header, updating each surviving slot's offset (its checksum is
// unchanged — compaction moves bytes verbatim, so the same bytes at a new
// offset still match the same CRC). Tombstoned slot entries stay put in the
// slot array unchanged — slot_count never shrinks here. This package always
// appends a brand-new slot entry for every insert (never reuses a
// tombstoned slot the way the catalog reuses tombstoned collection slots):
// compaction's whole point is reclaiming record BYTE space, not slot-array
// slots, which keeps the insert/compaction arithmetic in one place instead
// of two different "does this fit" paths.
func compactDataPage(view *dataPageView) error {
	newBuf := make([]byte, view.pageSize)
	writeOffset := uint32(dataPageHeaderSize)

	for i := uint32(0); i < uint32(view.slotCount); i++ {
		off, ok := dataSlotOffset(view.pageSize, i)
		if !ok {
			return ErrCorruptedDataPage
		}
		slot := decodeDataSlot(view.buf[off : off+dataSlotSize])
		if slot.isTombstone() {
			copy(newBuf[off:off+dataSlotSize], view.buf[off:off+dataSlotSize])
			continue
		}
		recBytes, err := view.recordBytes(slot)
		if err != nil {
			return err
		}
		copy(newBuf[writeOffset:writeOffset+uint32(slot.length)], recBytes)
		slot.offset = uint16(writeOffset)
		encodeDataSlot(newBuf[off:off+dataSlotSize], slot)
		writeOffset += uint32(slot.length)
	}

	view.buf = newBuf
	view.setHeader(view.next, uint16(writeOffset), view.slotCount)
	return view.finalize()
}
