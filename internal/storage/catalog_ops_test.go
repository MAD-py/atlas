package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

// smallCatalogPageSize gives catalogCapacity == 2, cheap to overflow a page
// in tests without needing hundreds of collections.
const smallCatalogPageSize = 200

type CatalogOpsSuite struct {
	suite.Suite
	f *os.File
	h *Header
}

func (s *CatalogOpsSuite) SetupTest() {
	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := Bootstrap(f, smallCatalogPageSize)
	s.Require().NoError(err)
	s.h = h
}

func (s *CatalogOpsSuite) TearDownTest() {
	s.Require().NoError(s.f.Close())
}

func TestCatalogOps(t *testing.T) {
	suite.Run(t, new(CatalogOpsSuite))
}

func (s *CatalogOpsSuite) corruptSlot(pageNum, index uint32) {
	buf, err := ReadPage(s.f, s.h.PageSize, pageNum)
	s.Require().NoError(err)
	off := catalogPageHeaderSize + index*collectionSlotSize
	buf[off] ^= 0xFF // flip the flags byte, invalidating the slot's checksum
	s.Require().NoError(WritePage(s.f, s.h.PageSize, pageNum, buf))
}

// --- create / find round trip ---

func (s *CatalogOpsSuite) TestCreateFind_RoundTrip() {
	ref, err := CreateCollectionSlot(s.f, s.h, "users", false)
	s.Require().NoError(err)
	s.Equal(s.h.CatalogHead, ref.Page)
	s.Equal(uint32(0), ref.Index)

	gotRef, slot, err := FindCollectionSlot(s.f, s.h, "users")
	s.Require().NoError(err)
	s.Equal(ref, gotRef)
	s.Equal("users", slot.Name)
	s.False(slot.IsTombstone())
	s.False(slot.IsInternal())
	s.Equal(uint32(0), slot.Head)
	s.Equal(uint32(0), slot.Tail)
	s.Equal(uint32(0), slot.PageCount)
	s.Equal(uint32(0), slot.DocCount)
}

func (s *CatalogOpsSuite) TestCreate_SetsInternalFlag() {
	_, err := CreateCollectionSlot(s.f, s.h, "_meta", true)
	s.Require().NoError(err)

	_, slot, err := FindCollectionSlot(s.f, s.h, "_meta")
	s.Require().NoError(err)
	s.True(slot.IsInternal())
}

func (s *CatalogOpsSuite) TestCreate_RejectsNameOver64Bytes() {
	_, err := CreateCollectionSlot(s.f, s.h, strings.Repeat("a", 65), false)
	s.ErrorIs(err, ErrCollectionNameTooLong)
}

func (s *CatalogOpsSuite) TestCreate_Accepts64ByteNameExactly() {
	_, err := CreateCollectionSlot(s.f, s.h, strings.Repeat("a", 64), false)
	s.NoError(err)
}

func (s *CatalogOpsSuite) TestCreate_RejectsDuplicateNameSamePage() {
	_, err := CreateCollectionSlot(s.f, s.h, "users", false)
	s.Require().NoError(err)

	_, err = CreateCollectionSlot(s.f, s.h, "users", false)
	s.ErrorIs(err, ErrCollectionAlreadyExists)
}

func (s *CatalogOpsSuite) TestCreate_RejectsDuplicateNameAcrossMultiPageChain() {
	_, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	_, err = CreateCollectionSlot(s.f, s.h, "b", false)
	s.Require().NoError(err) // anchor page (capacity 2) now full

	_, err = CreateCollectionSlot(s.f, s.h, "c", false)
	s.Require().NoError(err) // spills into a second catalog page
	s.Equal(uint32(2), s.h.PageCount)

	// "a" lives on the anchor page, well before the chain's tail (page 2)
	// — duplicate detection must scan the whole chain, not just the last
	// page reached.
	_, err = CreateCollectionSlot(s.f, s.h, "a", false)
	s.ErrorIs(err, ErrCollectionAlreadyExists)
}

func (s *CatalogOpsSuite) TestFind_NotFoundUnknownName() {
	_, _, err := FindCollectionSlot(s.f, s.h, "ghost")
	s.ErrorIs(err, ErrCollectionNotFound)
}

func (s *CatalogOpsSuite) TestFind_NotFoundAfterRemove() {
	_, err := CreateCollectionSlot(s.f, s.h, "users", false)
	s.Require().NoError(err)
	s.Require().NoError(RemoveCollectionSlot(s.f, s.h, "users"))

	_, _, err = FindCollectionSlot(s.f, s.h, "users")
	s.ErrorIs(err, ErrCollectionNotFound)
}

// --- chain growth ---

func (s *CatalogOpsSuite) TestCreate_GrowsChainWhenPageFull() {
	_, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	_, err = CreateCollectionSlot(s.f, s.h, "b", false)
	s.Require().NoError(err)
	s.Equal(uint32(1), s.h.PageCount) // still just the anchor

	ref, err := CreateCollectionSlot(s.f, s.h, "c", false)
	s.Require().NoError(err)
	s.Equal(uint32(2), s.h.PageCount) // grew
	s.NotEqual(s.h.CatalogHead, ref.Page)
	s.Equal(uint32(0), ref.Index)

	anchor, err := readCatalogPage(s.f, s.h, s.h.CatalogHead)
	s.Require().NoError(err)
	s.Equal(ref.Page, anchor.next)

	entries, err := ListCollectionSlots(s.f, s.h)
	s.Require().NoError(err)
	s.Len(entries, 3)
}

func (s *CatalogOpsSuite) TestCreate_ReusesTombstonedSlotBeforeGrowingChain() {
	_, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	refB, err := CreateCollectionSlot(s.f, s.h, "b", false)
	s.Require().NoError(err) // anchor (capacity 2) now full

	s.Require().NoError(RemoveCollectionSlot(s.f, s.h, "b"))

	refC, err := CreateCollectionSlot(s.f, s.h, "c", false)
	s.Require().NoError(err)

	s.Equal(refB, refC)               // reused b's tombstoned slot
	s.Equal(uint32(1), s.h.PageCount) // chain did not grow
}

// --- removal / free-list interaction ---

func (s *CatalogOpsSuite) TestRemove_FreesEmptyNonAnchorPage() {
	_, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	_, err = CreateCollectionSlot(s.f, s.h, "b", false)
	s.Require().NoError(err)
	refC, err := CreateCollectionSlot(s.f, s.h, "c", false) // grows to page 2
	s.Require().NoError(err)
	s.Require().NotEqual(s.h.CatalogHead, refC.Page)

	s.Require().NoError(RemoveCollectionSlot(s.f, s.h, "c"))

	s.Equal(refC.Page, s.h.FreeListHead)

	anchor, err := readCatalogPage(s.f, s.h, s.h.CatalogHead)
	s.Require().NoError(err)
	s.Equal(uint32(0), anchor.next) // unlinked

	// the freed page is available for reuse
	reused, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(refC.Page, reused)
}

func (s *CatalogOpsSuite) TestRemove_NeverFreesAnchorEvenWhenEmpty() {
	_, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)

	s.Require().NoError(RemoveCollectionSlot(s.f, s.h, "a"))

	s.Equal(uint32(0), s.h.FreeListHead)   // anchor not pushed to free-list
	s.Equal(uint32(1), s.h.PageCount)      // no page freed
	s.NotEqual(uint32(0), s.h.CatalogHead) // still points somewhere

	anchor, err := readCatalogPage(s.f, s.h, s.h.CatalogHead)
	s.Require().NoError(err)            // still decodes as a valid catalog page
	s.Equal(uint16(1), anchor.occupied) // slot stays occupied (tombstoned), not erased

	entries, err := ListCollectionSlots(s.f, s.h)
	s.Require().NoError(err)
	s.Empty(entries)
}

func (s *CatalogOpsSuite) TestRemove_UnknownCollection() {
	err := RemoveCollectionSlot(s.f, s.h, "ghost")
	s.ErrorIs(err, ErrCollectionNotFound)
}

// --- corruption resilience ---

func (s *CatalogOpsSuite) TestList_SkipsCorruptedSlotButListsRestAndReportsError() {
	_, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	refB, err := CreateCollectionSlot(s.f, s.h, "b", false)
	s.Require().NoError(err)

	s.corruptSlot(refB.Page, refB.Index)

	entries, err := ListCollectionSlots(s.f, s.h)
	s.Require().Error(err)
	s.ErrorIs(err, ErrCorruptedCollectionSlot)
	s.Require().Len(entries, 1)
	s.Equal("a", entries[0].Slot.Name)
}

func (s *CatalogOpsSuite) TestFind_MatchBeforeCorruptionInSameChainStillSucceeds() {
	refA, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	refB, err := CreateCollectionSlot(s.f, s.h, "b", false)
	s.Require().NoError(err)

	s.corruptSlot(refB.Page, refB.Index)

	gotRef, slot, err := FindCollectionSlot(s.f, s.h, "a")
	s.Require().NoError(err)
	s.Equal(refA, gotRef)
	s.Equal("a", slot.Name)
}

func (s *CatalogOpsSuite) TestFind_ReportsCorruptionInsteadOfFalseNotFound() {
	refA, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	s.corruptSlot(refA.Page, refA.Index)

	// "a"'s real name is unreadable now — a lookup for anything else that
	// reaches this slot cannot honestly claim "not found".
	_, _, err = FindCollectionSlot(s.f, s.h, "b")
	s.ErrorIs(err, ErrCorruptedCollectionSlot)
}

func (s *CatalogOpsSuite) TestCreate_DuplicateCheckFailsOnCorruptionRatherThanRiskingADuplicate() {
	refA, err := CreateCollectionSlot(s.f, s.h, "a", false)
	s.Require().NoError(err)
	s.corruptSlot(refA.Page, refA.Index)

	_, err = CreateCollectionSlot(s.f, s.h, "b", false)
	s.ErrorIs(err, ErrCorruptedCollectionSlot)
}

// --- direct in-place update ---

func (s *CatalogOpsSuite) TestWriteCollectionSlot_UpdatesInPlace() {
	ref, err := CreateCollectionSlot(s.f, s.h, "users", false)
	s.Require().NoError(err)

	_, slot, err := FindCollectionSlot(s.f, s.h, "users")
	s.Require().NoError(err)
	slot.Head = 5
	slot.Tail = 9
	slot.DocCount = 42
	s.Require().NoError(WriteCollectionSlot(s.f, s.h, ref, slot))

	_, got, err := FindCollectionSlot(s.f, s.h, "users")
	s.Require().NoError(err)
	s.Equal(uint32(5), got.Head)
	s.Equal(uint32(9), got.Tail)
	s.Equal(uint32(42), got.DocCount)
}

func (s *CatalogOpsSuite) TestWriteCollectionSlot_RejectsOutOfRangeIndex() {
	err := WriteCollectionSlot(s.f, s.h, SlotRef{Page: s.h.CatalogHead, Index: 5}, CollectionSlot{})
	s.ErrorIs(err, ErrInvalidSlotIndex)
}

// --- uninitialized catalog ---

func (s *CatalogOpsSuite) TestOps_RejectUninitializedCatalog() {
	uninit := &Header{PageSize: smallCatalogPageSize} // CatalogHead == 0

	_, err := CreateCollectionSlot(s.f, uninit, "x", false)
	s.ErrorIs(err, ErrCatalogNotInitialized)

	_, _, err = FindCollectionSlot(s.f, uninit, "x")
	s.ErrorIs(err, ErrCatalogNotInitialized)

	_, err = ListCollectionSlots(s.f, uninit)
	s.ErrorIs(err, ErrCatalogNotInitialized)
}

// --- corrupted chain (cycle) detection ---

// A corrupted next pointer forming a cycle must be reported as an error,
// not walked forever. Without maxCatalogChainLength, each of these would
// hang indefinitely instead of returning.
func (s *CatalogOpsSuite) TestChainWalks_DetectCycleInsteadOfHanging() {
	anchor := s.h.CatalogHead
	buf := make([]byte, s.h.PageSize)
	encodeCatalogPageHeader(buf, anchor, 0) // next points at itself
	s.Require().NoError(WritePage(s.f, s.h.PageSize, anchor, buf))

	_, _, err := FindCollectionSlot(s.f, s.h, "x")
	s.ErrorIs(err, ErrCorruptedCatalogChain)

	_, err = ListCollectionSlots(s.f, s.h)
	s.ErrorIs(err, ErrCorruptedCatalogChain)

	_, err = CreateCollectionSlot(s.f, s.h, "x", false)
	s.ErrorIs(err, ErrCorruptedCatalogChain)
}
