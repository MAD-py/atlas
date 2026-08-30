package storage

import (
	"context"
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
	f   *os.File
	h   *Header
	cyc *JournalCycle
}

func (s *DataOpsSuite) SetupTest() {
	s.cyc = nil // the suite instance is reused across tests; a prior test's committed cycle must not leak in

	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := Bootstrap(context.Background(), f, smallDataPageSize)
	s.Require().NoError(err)
	s.h = h
}

func (s *DataOpsSuite) TearDownTest() {
	if s.cyc != nil {
		_ = s.cyc.Commit(context.Background(), s.f, s.h)
	}
	s.Require().NoError(s.f.Close())
}

// cycle returns this test's single JournalCycle, opening one on first call
// and reusing it after — every mutating call in a test shares one cycle,
// committed automatically in TearDownTest.
func (s *DataOpsSuite) cycle() *JournalCycle {
	if s.cyc == nil {
		c, err := NewJournalCycle(context.Background(), s.f, s.h)
		s.Require().NoError(err)
		s.cyc = c
	}
	return s.cyc
}

func TestDataOps(t *testing.T) {
	suite.Run(t, new(DataOpsSuite))
}

// newCollection creates a fresh collection and returns its catalog ref
// alongside a zero-valued in-memory CollectionSlot mirroring what
// CreateCollectionSlot actually persisted.
func (s *DataOpsSuite) newCollection(name string) (SlotRef, CollectionSlot) {
	ref, err := CreateCollectionSlot(context.Background(), s.f, s.h, s.cycle(), name, false)
	s.Require().NoError(err)
	return ref, CollectionSlot{Name: name}
}

// insertN inserts count 12-byte records with sequential ids starting at
// startID into the collection, mutating slot in place.
func (s *DataOpsSuite) insertN(ref SlotRef, slot *CollectionSlot, startID, count int) {
	for i := range count {
		_, _, err := InsertRecord(context.Background(), s.f, s.h, s.cycle(), ref, slot, makeRecord(byte(startID+i), 0))
		s.Require().NoError(err)
	}
}

// --- insert: blank page, exact fill, new tail page ---

func (s *DataOpsSuite) TestInsert_IntoBlankPage() {
	ctx := context.Background()
	ref, slot := s.newCollection("docs")

	pageNum, idx, err := InsertRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, makeRecord(1, 0))
	s.Require().NoError(err)
	s.Equal(uint32(0), idx)
	s.Equal(slot.Head, pageNum)
	s.Equal(slot.Tail, pageNum)
	s.Equal(uint32(1), slot.PageCount)
	s.Equal(uint32(1), slot.DocCount)

	_, gotRef, err := FindCollectionSlot(ctx, s.f, s.h, "docs")
	s.Require().NoError(err)
	s.Equal(slot, gotRef)
}

func (s *DataOpsSuite) TestInsert_FillsPageExactly_ThenAllocatesNewTail() {
	ctx := context.Background()
	ref, slot := s.newCollection("docs")
	maxSize := dataPageMaxRecordSize(smallDataPageSize)

	page1, _, err := InsertRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, makeRecord(1, int(maxSize)-recordIDSize))
	s.Require().NoError(err)
	s.Equal(slot.Head, page1)
	s.Equal(slot.Tail, page1)
	s.Equal(uint32(1), slot.PageCount)

	view, err := readDataPage(ctx, s.f, s.h, page1)
	s.Require().NoError(err)
	wantSlotArrayStart, ok := dataSlotArrayStart(smallDataPageSize, 1) // one slot entry written
	s.Require().True(ok)
	s.Equal(uint16(wantSlotArrayStart), view.freeStart) // freeStart == slotArrayStart, zero gap left

	page2, _, err := InsertRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, makeRecord(2, 0))
	s.Require().NoError(err)
	s.NotEqual(page1, page2)
	s.Equal(page1, slot.Head)
	s.Equal(page2, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)
	s.Equal(uint32(2), slot.DocCount)

	page1After, err := readDataPage(ctx, s.f, s.h, page1)
	s.Require().NoError(err)
	s.Equal(page2, page1After.next)
}

func (s *DataOpsSuite) TestInsert_RejectsDocumentLargerThanBlankPage() {
	ctx := context.Background()
	ref, slot := s.newCollection("docs")
	maxSize := dataPageMaxRecordSize(smallDataPageSize)

	_, _, err := InsertRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, makeRecord(1, int(maxSize)-recordIDSize+1))
	s.ErrorIs(err, ErrDocumentTooLarge)
	s.Equal(uint32(0), slot.DocCount) // rejected before touching any page
}

// --- insert: compaction path ---

func (s *DataOpsSuite) TestInsert_CompactsPageWhenTombstonedSpaceWouldFitButContiguousWouldNot() {
	ctx := context.Background()
	ref, slot := s.newCollection("compact")

	s.insertN(ref, &slot, 101, 3) // ids 101,102,103 — fills contiguous gap to 16 bytes
	s.Require().NoError(DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(102)))

	// 19-byte record: doesn't fit the 7-byte contiguous gap left after the
	// new slot entry is accounted for, but does fit exactly once the
	// tombstoned record's 12 bytes are reclaimed by compaction.
	pageNum, _, err := InsertRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, makeRecord(104, 19-recordIDSize))
	s.Require().NoError(err)

	s.Equal(uint32(1), slot.PageCount) // compaction reused the page, no new page allocated
	s.Equal(uint32(3), slot.DocCount)  // 3 inserted, 1 deleted, 1 inserted

	view, err := readDataPage(ctx, s.f, s.h, pageNum)
	s.Require().NoError(err)
	s.Equal(uint16(4), view.slotCount) // tombstoned slot entry still counted
	live, err := view.liveSlotCount()
	s.Require().NoError(err)
	s.Equal(uint32(3), live)
	s.NoError(verifyDataPageChecksum(view.buf, view.pageSize))

	_, _, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(102))
	s.ErrorIs(err, ErrDocumentNotFound)
	for _, id := range []byte{101, 103, 104} {
		rec, _, _, ferr := FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(id))
		s.Require().NoError(ferr)
		s.Equal(id, rec[0])
	}
}

// --- FindRecordByID ---

func (s *DataOpsSuite) TestFind_HitAndMissAcrossMultiPageChain() {
	ctx := context.Background()
	ref, slot := s.newCollection("find")
	s.insertN(ref, &slot, 1, 5) // spills across 2 pages (capacity 3/page here)
	s.Require().Equal(uint32(2), slot.PageCount)

	rec, pageNum, idx, err := FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(1))
	s.Require().NoError(err)
	s.Equal(slot.Head, pageNum)
	s.Equal(uint32(0), idx)
	s.Equal(makeRecord(1, 0), rec)

	rec, pageNum, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(5))
	s.Require().NoError(err)
	s.Equal(slot.Tail, pageNum)
	s.Equal(makeRecord(5, 0), rec)

	_, _, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(99))
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *DataOpsSuite) TestFind_EmptyCollectionHeadZero() {
	ctx := context.Background()
	_, slot := s.newCollection("empty")
	_, _, _, err := FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(1))
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *DataOpsSuite) TestFind_DetectsCorruptedRecordAfterIDMatch() {
	ctx := context.Background()
	ref, slot := s.newCollection("corrupt")
	_, _, err := InsertRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, makeRecord(1, 8))
	s.Require().NoError(err)

	view, err := readDataPage(ctx, s.f, s.h, slot.Head)
	s.Require().NoError(err)
	rSlot, err := view.slotAt(0)
	s.Require().NoError(err)
	view.buf[rSlot.offset+recordIDSize] ^= 0xFF // corrupt payload bytes past the id
	s.Require().NoError(view.finalize())
	s.Require().NoError(WritePage(ctx, s.f, s.h, s.cycle(), slot.Head, view.buf))

	_, _, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(1))
	s.ErrorIs(err, ErrCorruptedDocument)
}

// --- DeleteRecord ---

func (s *DataOpsSuite) TestDelete_TombstonesWithoutFreeingPageWhenOthersRemainLive() {
	ctx := context.Background()
	ref, slot := s.newCollection("del1")
	s.insertN(ref, &slot, 1, 3)
	page := slot.Head

	s.Require().NoError(DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(2)))
	s.Equal(uint32(2), slot.DocCount)
	s.Equal(page, slot.Head)
	s.Equal(page, slot.Tail)
	s.Equal(uint32(1), slot.PageCount)

	_, err := readDataPage(ctx, s.f, s.h, page) // page still valid, not freed
	s.Require().NoError(err)

	_, _, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(2))
	s.ErrorIs(err, ErrDocumentNotFound)
	_, _, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(1))
	s.NoError(err)
}

func (s *DataOpsSuite) TestDelete_UnknownID() {
	ctx := context.Background()
	ref, slot := s.newCollection("del-miss")
	s.insertN(ref, &slot, 1, 1)
	err := DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(99))
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *DataOpsSuite) TestDelete_EmptyingOnlyPage_HeadAndTailBothBecomeZero() {
	ctx := context.Background()
	ref, slot := s.newCollection("del-only")
	s.insertN(ref, &slot, 1, 1)
	page := slot.Head

	s.Require().NoError(DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(1)))

	s.Equal(uint32(0), slot.Head)
	s.Equal(uint32(0), slot.Tail)
	s.Equal(uint32(0), slot.PageCount)
	s.Equal(uint32(0), slot.DocCount)
	s.Equal(page, s.h.FreeListHead) // pushed onto the free-list

	_, gotSlot, err := FindCollectionSlot(ctx, s.f, s.h, "del-only")
	s.Require().NoError(err)
	s.Equal(uint32(0), gotSlot.Head)
	s.Equal(uint32(0), gotSlot.Tail)
}

func (s *DataOpsSuite) TestDelete_EmptyingHeadPage_AdvancesHeadKeepsTail() {
	ctx := context.Background()
	ref, slot := s.newCollection("del-head")
	s.insertN(ref, &slot, 1, 9) // 3 pages of 3 records each: A(1,2,3) B(4,5,6) C(7,8,9)
	s.Require().Equal(uint32(3), slot.PageCount)
	pageA, pageC := slot.Head, slot.Tail

	for _, id := range []byte{1, 2, 3} {
		s.Require().NoError(DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(id)))
	}

	s.NotEqual(pageA, slot.Head)
	s.Equal(pageC, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)
	s.Equal(uint32(6), slot.DocCount)
	s.Equal(pageA, s.h.FreeListHead)

	rec, _, _, err := FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(4))
	s.Require().NoError(err)
	s.Equal(byte(4), rec[0])
}

func (s *DataOpsSuite) TestDelete_EmptyingTailPage_RetreatsTailKeepsHead() {
	ctx := context.Background()
	ref, slot := s.newCollection("del-tail")
	s.insertN(ref, &slot, 1, 9) // A(1,2,3) B(4,5,6) C(7,8,9)
	pageA, pageC := slot.Head, slot.Tail

	for _, id := range []byte{7, 8, 9} {
		s.Require().NoError(DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(id)))
	}

	s.Equal(pageA, slot.Head)
	s.NotEqual(pageC, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)
	s.Equal(pageC, s.h.FreeListHead)

	tailView, err := readDataPage(ctx, s.f, s.h, slot.Tail)
	s.Require().NoError(err)
	s.Equal(uint32(0), tailView.next) // new tail terminates the chain
}

func (s *DataOpsSuite) TestDelete_EmptyingMiddlePage_RelinksAroundIt() {
	ctx := context.Background()
	ref, slot := s.newCollection("del-mid")
	s.insertN(ref, &slot, 1, 9) // A(1,2,3) B(4,5,6) C(7,8,9)
	pageA, pageC := slot.Head, slot.Tail

	for _, id := range []byte{4, 5, 6} {
		s.Require().NoError(DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(id)))
	}

	s.Equal(pageA, slot.Head)
	s.Equal(pageC, slot.Tail)
	s.Equal(uint32(2), slot.PageCount)

	aView, err := readDataPage(ctx, s.f, s.h, pageA)
	s.Require().NoError(err)
	s.Equal(pageC, aView.next) // B unlinked from the chain

	// the freed page is available for reuse
	reused, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	s.NotEqual(pageA, reused)
	s.NotEqual(pageC, reused)
}

// --- FreeCollectionDataPages ---

func (s *DataOpsSuite) TestFreeCollectionDataPages_EmptyCollectionIsNoop() {
	ctx := context.Background()
	beforeFreeListHead := s.h.FreeListHead

	err := FreeCollectionDataPages(ctx, s.f, s.h, s.cycle(), 0)
	s.Require().NoError(err)
	s.Equal(beforeFreeListHead, s.h.FreeListHead)
}

func (s *DataOpsSuite) TestFreeCollectionDataPages_SinglePage() {
	ctx := context.Background()
	ref, slot := s.newCollection("single")
	s.insertN(ref, &slot, 1, 1)
	page := slot.Head

	err := FreeCollectionDataPages(ctx, s.f, s.h, s.cycle(), slot.Head)
	s.Require().NoError(err)
	s.Equal(page, s.h.FreeListHead)
}

func (s *DataOpsSuite) TestFreeCollectionDataPages_MultiPageChain() {
	ctx := context.Background()
	ref, slot := s.newCollection("multi")
	s.insertN(ref, &slot, 1, 9) // 3 pages of 3 records each
	s.Require().Equal(uint32(3), slot.PageCount)

	var original []uint32
	for pageNum := slot.Head; pageNum != 0; {
		original = append(original, pageNum)
		view, err := readDataPage(ctx, s.f, s.h, pageNum)
		s.Require().NoError(err)
		pageNum = view.next
	}
	s.Require().Len(original, 3)

	s.Require().NoError(FreeCollectionDataPages(ctx, s.f, s.h, s.cycle(), slot.Head))

	// every original page is now reachable through the free-list
	var reused []uint32
	for range original {
		p, err := Allocate(ctx, s.f, s.h, s.cycle())
		s.Require().NoError(err)
		reused = append(reused, p)
	}
	s.ElementsMatch(original, reused)
}

// Unlike the read-only/tombstone-only walks above, this walk frees each
// page as it visits it, so a self-loop can never be re-read as a live data
// page the second time around — it surfaces as ErrNotADataPage on the
// revisit, not the maxDataChainLength bound. Either way the walk
// terminates instead of hanging, which is what this test actually proves.
func (s *DataOpsSuite) TestFreeCollectionDataPages_DetectsCycleInsteadOfHanging() {
	ctx := context.Background()

	pageNum, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	view, err := newBlankDataPage(s.h.PageSize)
	s.Require().NoError(err)
	view.setHeader(pageNum, view.freeStart, view.slotCount) // next points at itself
	s.Require().NoError(view.finalize())
	s.Require().NoError(WritePage(ctx, s.f, s.h, s.cycle(), pageNum, view.buf))

	err = FreeCollectionDataPages(ctx, s.f, s.h, s.cycle(), pageNum)
	s.ErrorIs(err, ErrNotADataPage)
}

// --- readDataPage: wrong page type ---

func (s *DataOpsSuite) TestReadDataPage_RejectsNonDataPage() {
	_, err := readDataPage(context.Background(), s.f, s.h, s.h.CatalogHead) // a catalog page, not a data page
	s.ErrorIs(err, ErrNotADataPage)
}

// --- corrupted chain (cycle) detection ---

func (s *DataOpsSuite) TestChainWalks_DetectCycleInsteadOfHanging() {
	ctx := context.Background()
	ref, slot := s.newCollection("cycle")

	pageNum, err := Allocate(ctx, s.f, s.h, s.cycle())
	s.Require().NoError(err)
	view, err := newBlankDataPage(s.h.PageSize)
	s.Require().NoError(err)
	view.setHeader(pageNum, view.freeStart, view.slotCount) // next points at itself
	s.Require().NoError(view.finalize())
	s.Require().NoError(WritePage(ctx, s.f, s.h, s.cycle(), pageNum, view.buf))

	slot.Head, slot.Tail, slot.PageCount = pageNum, pageNum, 1
	s.Require().NoError(WriteCollectionSlot(ctx, s.f, s.h, s.cycle(), ref, slot))

	_, _, _, err = FindRecordByID(ctx, s.f, s.h, slot.Head, recordID(1))
	s.ErrorIs(err, ErrCorruptedDataChain)

	err = DeleteRecord(ctx, s.f, s.h, s.cycle(), ref, &slot, recordID(1))
	s.ErrorIs(err, ErrCorruptedDataChain)
}
