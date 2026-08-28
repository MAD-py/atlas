package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

// smallDataPageSize keeps data pages tiny (max record 70 bytes, ~3 records
// of 12 bytes per page) so multi-page chains, exact-fill, and compaction
// paths are all cheap to trigger without needing hundreds of documents.
// Also large enough for the catalog to bootstrap ((92-7)/85 == 1 slot/page).
const smallDataPageSize = 92

type DataOpsSuite struct {
	suite.Suite
	f *os.File
	h *Header
}

func (s *DataOpsSuite) SetupTest() {
	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := Bootstrap(f, smallDataPageSize)
	s.Require().NoError(err)
	s.h = h
}

func (s *DataOpsSuite) TearDownTest() {
	s.Require().NoError(s.f.Close())
}

func TestDataOps(t *testing.T) {
	suite.Run(t, new(DataOpsSuite))
}

// newCollection creates a fresh collection and returns its catalog ref
// alongside a zero-valued in-memory CollectionSlot mirroring what
// CreateCollectionSlot actually persisted.
func (s *DataOpsSuite) newCollection(name string) (SlotRef, CollectionSlot) {
	ref, err := CreateCollectionSlot(s.f, s.h, name, false)
	s.Require().NoError(err)
	return ref, CollectionSlot{Name: name}
}

// insertN inserts count 12-byte records with sequential ids starting at
// startID into the collection, mutating slot in place.
func (s *DataOpsSuite) insertN(ref SlotRef, slot *CollectionSlot, startID, count int) {
	for i := 0; i < count; i++ {
		_, _, err := InsertRecord(s.f, s.h, ref, slot, makeRecord(byte(startID+i), 0))
		s.Require().NoError(err)
	}
}

// --- insert: blank page, exact fill, new tail page ---

func (s *DataOpsSuite) TestInsert_IntoBlankPage() {
	ref, slot := s.newCollection("docs")

	pageNum, idx, err := InsertRecord(s.f, s.h, ref, &slot, makeRecord(1, 0))
	s.Require().NoError(err)
	s.Equal(uint32(0), idx)
	s.Equal(slot.Head, pageNum)
	s.Equal(slot.Tail, pageNum)
	s.Equal(uint32(1), slot.PageCount)
	s.Equal(uint32(1), slot.DocCount)

	_, gotRef, err := FindCollectionSlot(s.f, s.h, "docs")
	s.Require().NoError(err)
	s.Equal(slot, gotRef)
}

func (s *DataOpsSuite) TestInsert_FillsPageExactly_ThenAllocatesNewTail() {
	ref, slot := s.newCollection("docs")
	maxSize := dataPageMaxRecordSize(smallDataPageSize)

	page1, _, err := InsertRecord(s.f, s.h, ref, &slot, makeRecord(1, int(maxSize)-recordIDSize))
	s.Require().NoError(err)
	s.Equal(slot.Head, page1)
	s.Equal(slot.Tail, page1)
	s.Equal(uint32(1), slot.PageCount)

	view, err := readDataPage(s.f, s.h, page1)
	s.Require().NoError(err)
	wantSlotArrayStart, ok := dataSlotArrayStart(smallDataPageSize, 1) // one slot entry written
	s.Require().True(ok)
	s.Equal(uint16(wantSlotArrayStart), view.freeStart) // freeStart == slotArrayStart, zero gap left

	page2, _, err := InsertRecord(s.f, s.h, ref, &slot, makeRecord(2, 0))
	s.Require().NoError(err)
	s.NotEqual(page1, page2)
	s.Equal(page1, slot.Head)
	s.Equal(page2, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)
	s.Equal(uint32(2), slot.DocCount)

	page1After, err := readDataPage(s.f, s.h, page1)
	s.Require().NoError(err)
	s.Equal(page2, page1After.next)
}

func (s *DataOpsSuite) TestInsert_RejectsDocumentLargerThanBlankPage() {
	ref, slot := s.newCollection("docs")
	maxSize := dataPageMaxRecordSize(smallDataPageSize)

	_, _, err := InsertRecord(s.f, s.h, ref, &slot, makeRecord(1, int(maxSize)-recordIDSize+1))
	s.ErrorIs(err, ErrDocumentTooLarge)
	s.Equal(uint32(0), slot.DocCount) // rejected before touching any page
}

// --- insert: compaction path ---

func (s *DataOpsSuite) TestInsert_CompactsPageWhenTombstonedSpaceWouldFitButContiguousWouldNot() {
	ref, slot := s.newCollection("compact")

	s.insertN(ref, &slot, 101, 3) // ids 101,102,103 — fills contiguous gap to 16 bytes
	s.Require().NoError(DeleteRecord(s.f, s.h, ref, &slot, recordID(102)))

	// 19-byte record: doesn't fit the 7-byte contiguous gap left after the
	// new slot entry is accounted for, but does fit exactly once the
	// tombstoned record's 12 bytes are reclaimed by compaction.
	pageNum, _, err := InsertRecord(s.f, s.h, ref, &slot, makeRecord(104, 19-recordIDSize))
	s.Require().NoError(err)

	s.Equal(uint32(1), slot.PageCount) // compaction reused the page, no new page allocated
	s.Equal(uint32(3), slot.DocCount)  // 3 inserted, 1 deleted, 1 inserted

	view, err := readDataPage(s.f, s.h, pageNum)
	s.Require().NoError(err)
	s.Equal(uint16(4), view.slotCount) // tombstoned slot entry still counted
	live, err := view.liveSlotCount()
	s.Require().NoError(err)
	s.Equal(uint32(3), live)
	s.NoError(verifyDataPageChecksum(view.buf, view.pageSize))

	_, _, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(102))
	s.ErrorIs(err, ErrDocumentNotFound)
	for _, id := range []byte{101, 103, 104} {
		rec, _, _, ferr := FindRecordByID(s.f, s.h, slot.Head, recordID(id))
		s.Require().NoError(ferr)
		s.Equal(id, rec[0])
	}
}

// --- FindRecordByID ---

func (s *DataOpsSuite) TestFind_HitAndMissAcrossMultiPageChain() {
	ref, slot := s.newCollection("find")
	s.insertN(ref, &slot, 1, 5) // spills across 2 pages (capacity 3/page here)
	s.Require().Equal(uint32(2), slot.PageCount)

	rec, pageNum, idx, err := FindRecordByID(s.f, s.h, slot.Head, recordID(1))
	s.Require().NoError(err)
	s.Equal(slot.Head, pageNum)
	s.Equal(uint32(0), idx)
	s.Equal(makeRecord(1, 0), rec)

	rec, pageNum, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(5))
	s.Require().NoError(err)
	s.Equal(slot.Tail, pageNum)
	s.Equal(makeRecord(5, 0), rec)

	_, _, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(99))
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *DataOpsSuite) TestFind_EmptyCollectionHeadZero() {
	_, slot := s.newCollection("empty")
	_, _, _, err := FindRecordByID(s.f, s.h, slot.Head, recordID(1))
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *DataOpsSuite) TestFind_DetectsCorruptedRecordAfterIDMatch() {
	ref, slot := s.newCollection("corrupt")
	_, _, err := InsertRecord(s.f, s.h, ref, &slot, makeRecord(1, 8))
	s.Require().NoError(err)

	view, err := readDataPage(s.f, s.h, slot.Head)
	s.Require().NoError(err)
	rSlot, err := view.slotAt(0)
	s.Require().NoError(err)
	view.buf[rSlot.offset+recordIDSize] ^= 0xFF // corrupt payload bytes past the id
	s.Require().NoError(view.finalize())
	s.Require().NoError(WritePage(s.f, s.h.PageSize, slot.Head, view.buf))

	_, _, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(1))
	s.ErrorIs(err, ErrCorruptedDocument)
}

// --- DeleteRecord ---

func (s *DataOpsSuite) TestDelete_TombstonesWithoutFreeingPageWhenOthersRemainLive() {
	ref, slot := s.newCollection("del1")
	s.insertN(ref, &slot, 1, 3)
	page := slot.Head

	s.Require().NoError(DeleteRecord(s.f, s.h, ref, &slot, recordID(2)))
	s.Equal(uint32(2), slot.DocCount)
	s.Equal(page, slot.Head)
	s.Equal(page, slot.Tail)
	s.Equal(uint32(1), slot.PageCount)

	_, err := readDataPage(s.f, s.h, page) // page still valid, not freed
	s.Require().NoError(err)

	_, _, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(2))
	s.ErrorIs(err, ErrDocumentNotFound)
	_, _, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(1))
	s.NoError(err)
}

func (s *DataOpsSuite) TestDelete_UnknownID() {
	ref, slot := s.newCollection("del-miss")
	s.insertN(ref, &slot, 1, 1)
	err := DeleteRecord(s.f, s.h, ref, &slot, recordID(99))
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *DataOpsSuite) TestDelete_EmptyingOnlyPage_HeadAndTailBothBecomeZero() {
	ref, slot := s.newCollection("del-only")
	s.insertN(ref, &slot, 1, 1)
	page := slot.Head

	s.Require().NoError(DeleteRecord(s.f, s.h, ref, &slot, recordID(1)))

	s.Equal(uint32(0), slot.Head)
	s.Equal(uint32(0), slot.Tail)
	s.Equal(uint32(0), slot.PageCount)
	s.Equal(uint32(0), slot.DocCount)
	s.Equal(page, s.h.FreeListHead) // pushed onto the free-list

	_, gotSlot, err := FindCollectionSlot(s.f, s.h, "del-only")
	s.Require().NoError(err)
	s.Equal(uint32(0), gotSlot.Head)
	s.Equal(uint32(0), gotSlot.Tail)
}

func (s *DataOpsSuite) TestDelete_EmptyingHeadPage_AdvancesHeadKeepsTail() {
	ref, slot := s.newCollection("del-head")
	s.insertN(ref, &slot, 1, 9) // 3 pages of 3 records each: A(1,2,3) B(4,5,6) C(7,8,9)
	s.Require().Equal(uint32(3), slot.PageCount)
	pageA, pageC := slot.Head, slot.Tail

	for _, id := range []byte{1, 2, 3} {
		s.Require().NoError(DeleteRecord(s.f, s.h, ref, &slot, recordID(id)))
	}

	s.NotEqual(pageA, slot.Head)
	s.Equal(pageC, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)
	s.Equal(uint32(6), slot.DocCount)
	s.Equal(pageA, s.h.FreeListHead)

	rec, _, _, err := FindRecordByID(s.f, s.h, slot.Head, recordID(4))
	s.Require().NoError(err)
	s.Equal(byte(4), rec[0])
}

func (s *DataOpsSuite) TestDelete_EmptyingTailPage_RetreatsTailKeepsHead() {
	ref, slot := s.newCollection("del-tail")
	s.insertN(ref, &slot, 1, 9) // A(1,2,3) B(4,5,6) C(7,8,9)
	pageA, pageC := slot.Head, slot.Tail

	for _, id := range []byte{7, 8, 9} {
		s.Require().NoError(DeleteRecord(s.f, s.h, ref, &slot, recordID(id)))
	}

	s.Equal(pageA, slot.Head)
	s.NotEqual(pageC, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)
	s.Equal(pageC, s.h.FreeListHead)

	tailView, err := readDataPage(s.f, s.h, slot.Tail)
	s.Require().NoError(err)
	s.Equal(uint32(0), tailView.next) // new tail terminates the chain
}

func (s *DataOpsSuite) TestDelete_EmptyingMiddlePage_RelinksAroundIt() {
	ref, slot := s.newCollection("del-mid")
	s.insertN(ref, &slot, 1, 9) // A(1,2,3) B(4,5,6) C(7,8,9)
	pageA, pageC := slot.Head, slot.Tail

	for _, id := range []byte{4, 5, 6} {
		s.Require().NoError(DeleteRecord(s.f, s.h, ref, &slot, recordID(id)))
	}

	s.Equal(pageA, slot.Head)
	s.Equal(pageC, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)

	aView, err := readDataPage(s.f, s.h, pageA)
	s.Require().NoError(err)
	s.Equal(pageC, aView.next) // B unlinked from the chain

	// the freed page is available for reuse
	reused, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.NotEqual(pageA, reused)
	s.NotEqual(pageC, reused)
}

// --- readDataPage: wrong page type ---

func (s *DataOpsSuite) TestReadDataPage_RejectsNonDataPage() {
	_, err := readDataPage(s.f, s.h, s.h.CatalogHead) // a catalog page, not a data page
	s.ErrorIs(err, ErrNotADataPage)
}

// --- corrupted chain (cycle) detection ---

func (s *DataOpsSuite) TestChainWalks_DetectCycleInsteadOfHanging() {
	ref, slot := s.newCollection("cycle")

	pageNum, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	view, err := newBlankDataPage(s.h.PageSize)
	s.Require().NoError(err)
	view.setHeader(pageNum, view.freeStart, view.slotCount) // next points at itself
	s.Require().NoError(view.finalize())
	s.Require().NoError(WritePage(s.f, s.h.PageSize, pageNum, view.buf))

	slot.Head, slot.Tail, slot.PageCount = pageNum, pageNum, 1
	s.Require().NoError(WriteCollectionSlot(s.f, s.h, ref, slot))

	_, _, _, err = FindRecordByID(s.f, s.h, slot.Head, recordID(1))
	s.ErrorIs(err, ErrCorruptedDataChain)

	err = DeleteRecord(s.f, s.h, ref, &slot, recordID(1))
	s.ErrorIs(err, ErrCorruptedDataChain)
}
