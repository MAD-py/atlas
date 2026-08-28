package storage

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/suite"
)

type DataPageSuite struct {
	suite.Suite
}

func TestDataPage(t *testing.T) {
	suite.Run(t, new(DataPageSuite))
}

// --- header + slot codec round trip ---

func (s *DataPageSuite) TestDataPageHeaderFields_EncodeDecode_RoundTrip() {
	buf := make([]byte, 64)
	encodeDataPageHeaderFields(buf, 42, 100, 3)
	binary.BigEndian.PutUint32(buf[9:13], 0xDEADBEEF)

	next, freeStart, slotCount, checksum, err := decodeDataPageHeaderFields(buf)
	s.Require().NoError(err)
	s.Equal(uint32(42), next)
	s.Equal(uint16(100), freeStart)
	s.Equal(uint16(3), slotCount)
	s.Equal(uint32(0xDEADBEEF), checksum)
}

func (s *DataPageSuite) TestDecodeDataPageHeaderFields_RejectsWrongPageType() {
	buf := make([]byte, 64)
	buf[0] = byte(PageTypeCatalog)
	_, _, _, _, err := decodeDataPageHeaderFields(buf)
	s.ErrorIs(err, ErrNotADataPage)
}

func (s *DataPageSuite) TestDecodeDataPageHeaderFields_RejectsShortBuffer() {
	_, _, _, _, err := decodeDataPageHeaderFields(make([]byte, dataPageHeaderSize-1))
	s.ErrorIs(err, ErrShortPageRead)
}

func (s *DataPageSuite) TestDataSlot_EncodeDecode_RoundTrip() {
	buf := make([]byte, dataSlotSize)
	want := dataSlot{flags: dataSlotFlagTombstone, offset: 13, length: 40, checksum: 0xCAFEBABE}
	encodeDataSlot(buf, want)

	got := decodeDataSlot(buf)
	s.Equal(want, got)
	s.True(got.isTombstone())
}

func (s *DataPageSuite) TestDataSlotArrayStart_DetectsUnderflow() {
	_, ok := dataSlotArrayStart(64, 1000) // 1000*9 far exceeds 64
	s.False(ok)
}

func (s *DataPageSuite) TestDataSlotArrayStart_ValidRange() {
	start, ok := dataSlotArrayStart(120, 3)
	s.Require().True(ok)
	s.Equal(uint32(120-3*dataSlotSize), start)
}

func (s *DataPageSuite) TestDataSlotOffset_MatchesArrayStartOfOneMoreSlot() {
	off, ok := dataSlotOffset(120, 2) // slot index 2 == the start of a 3-slot array
	s.Require().True(ok)
	want, _ := dataSlotArrayStart(120, 3)
	s.Equal(want, off)
}

// --- page_checksum coverage: header + slot array + live records only ---

func (s *DataPageSuite) newTestPage(pageSize uint32) *dataPageView {
	view, err := newBlankDataPage(pageSize)
	s.Require().NoError(err)
	return view
}

func (s *DataPageSuite) TestChecksum_TombstonedRecordBytesChanging_DoesNotInvalidatePage() {
	view := s.newTestPage(92)

	_, err := writeRecordIntoPage(view, makeRecord(1, 0))
	s.Require().NoError(err)
	idx2, err := writeRecordIntoPage(view, makeRecord(2, 0))
	s.Require().NoError(err)

	// Tombstone record 2 directly (bypassing DeleteRecord — this test is
	// about checksum coverage, not the delete operation).
	off, ok := dataSlotOffset(view.pageSize, idx2)
	s.Require().True(ok)
	slot2 := decodeDataSlot(view.buf[off : off+dataSlotSize])
	slot2.flags |= dataSlotFlagTombstone
	encodeDataSlot(view.buf[off:off+dataSlotSize], slot2)
	s.Require().NoError(view.finalize())

	s.Require().NoError(verifyDataPageChecksum(view.buf, view.pageSize))

	// Corrupt the tombstoned record's underlying bytes without touching the
	// stored page_checksum — it must still validate, since those bytes are
	// excluded from the coverage.
	view.buf[slot2.offset] ^= 0xFF
	s.NoError(verifyDataPageChecksum(view.buf, view.pageSize))
}

func (s *DataPageSuite) TestChecksum_LiveRecordBytesChanging_InvalidatesPage() {
	view := s.newTestPage(92)

	idx1, err := writeRecordIntoPage(view, makeRecord(1, 0))
	s.Require().NoError(err)
	_, err = writeRecordIntoPage(view, makeRecord(2, 0))
	s.Require().NoError(err)

	s.Require().NoError(verifyDataPageChecksum(view.buf, view.pageSize))

	off, ok := dataSlotOffset(view.pageSize, idx1)
	s.Require().True(ok)
	slot1 := decodeDataSlot(view.buf[off : off+dataSlotSize])

	view.buf[slot1.offset] ^= 0xFF // corrupt a live record's bytes
	s.ErrorIs(verifyDataPageChecksum(view.buf, view.pageSize), ErrCorruptedPage)
}

func (s *DataPageSuite) TestChecksum_HeaderCorruption_InvalidatesPage() {
	view := s.newTestPage(92)
	_, err := writeRecordIntoPage(view, makeRecord(1, 0))
	s.Require().NoError(err)

	view.buf[1] ^= 0xFF // corrupt next_page field
	s.ErrorIs(verifyDataPageChecksum(view.buf, view.pageSize), ErrCorruptedPage)
}

// --- blank page / max record size ---

func (s *DataPageSuite) TestNewBlankDataPage_StartsEmptyAndValidates() {
	view := s.newTestPage(92)
	s.Equal(uint32(0), view.next)
	s.Equal(uint16(dataPageHeaderSize), view.freeStart)
	s.Equal(uint16(0), view.slotCount)
	s.NoError(verifyDataPageChecksum(view.buf, view.pageSize))
}

func (s *DataPageSuite) TestDataPageMaxRecordSize_HeaderAndOneSlotReserved() {
	s.Equal(uint32(92-dataPageHeaderSize-dataSlotSize), dataPageMaxRecordSize(92))
}

// --- compaction ---

func (s *DataPageSuite) TestCompactDataPage_RelocatesLiveRecordsAndPreservesChecksums() {
	view := s.newTestPage(92)

	idx0, err := writeRecordIntoPage(view, makeRecord(0, 0))
	s.Require().NoError(err)
	idx1, err := writeRecordIntoPage(view, makeRecord(1, 0))
	s.Require().NoError(err)
	idx2, err := writeRecordIntoPage(view, makeRecord(2, 0))
	s.Require().NoError(err)

	// tombstone the middle record
	off1, _ := dataSlotOffset(view.pageSize, idx1)
	s1 := decodeDataSlot(view.buf[off1 : off1+dataSlotSize])
	s1.flags |= dataSlotFlagTombstone
	encodeDataSlot(view.buf[off1:off1+dataSlotSize], s1)
	s.Require().NoError(view.finalize())

	preTombstoneChecksum := func(idx uint32) uint32 {
		off, _ := dataSlotOffset(view.pageSize, idx)
		return decodeDataSlot(view.buf[off : off+dataSlotSize]).checksum
	}
	c0Before, c2Before := preTombstoneChecksum(idx0), preTombstoneChecksum(idx2)

	s.Require().NoError(compactDataPage(view))

	s.Equal(uint16(3), view.slotCount) // tombstoned slot entry still counted
	live, err := view.liveSlotCount()
	s.Require().NoError(err)
	s.Equal(uint32(2), live)

	// surviving records relocated contiguously right after the header
	s0, err := view.slotAt(idx0)
	s.Require().NoError(err)
	s2, err := view.slotAt(idx2)
	s.Require().NoError(err)
	s.Equal(uint16(dataPageHeaderSize), s0.offset)
	s.Equal(uint16(dataPageHeaderSize)+s0.length, s2.offset)

	// checksums unchanged — same bytes, just moved
	s.Equal(c0Before, s0.checksum)
	s.Equal(c2Before, s2.checksum)

	s.Equal(uint16(dataPageHeaderSize)+s0.length+s2.length, view.freeStart)
	s.NoError(verifyDataPageChecksum(view.buf, view.pageSize))
}

// makeRecord builds an opaque record whose first recordIDSize bytes encode
// idByte repeated (a stand-in for a real AtlasID, which this package never
// interprets), followed by extraLen filler bytes.
func makeRecord(idByte byte, extraLen int) []byte {
	rec := make([]byte, recordIDSize+extraLen)
	for i := 0; i < recordIDSize; i++ {
		rec[i] = idByte
	}
	for i := 0; i < extraLen; i++ {
		rec[recordIDSize+i] = byte(i)
	}
	return rec
}

func recordID(idByte byte) [recordIDSize]byte {
	var id [recordIDSize]byte
	for i := range id {
		id[i] = idByte
	}
	return id
}
