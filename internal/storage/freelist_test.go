package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type FreeListSuite struct {
	suite.Suite
	f *os.File
	h *Header
}

func (s *FreeListSuite) SetupTest() {
	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f

	h, err := NewHeader(256)
	s.Require().NoError(err)
	s.Require().NoError(WriteHeader(f, h))
	s.h = h
}

func (s *FreeListSuite) TearDownTest() {
	s.Require().NoError(s.f.Close())
}

func TestFreeList(t *testing.T) {
	suite.Run(t, new(FreeListSuite))
}

func (s *FreeListSuite) TestAllocate_GrowsFileWhenFreeListEmpty() {
	p1, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(uint32(1), p1)
	s.Equal(uint32(1), s.h.PageCount)
	s.Equal(uint32(0), s.h.FreeListHead)

	p2, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(uint32(2), p2)
	s.Equal(uint32(2), s.h.PageCount)
}

func (s *FreeListSuite) TestAllocateFree_ReusesFreedPageBeforeGrowing() {
	_, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	p2, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	_, err = Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(uint32(3), s.h.PageCount)

	s.Require().NoError(Free(s.f, s.h, p2))
	s.Equal(p2, s.h.FreeListHead)

	got, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(p2, got)
	s.Equal(uint32(3), s.h.PageCount) // reused, not grown
	s.Equal(uint32(0), s.h.FreeListHead)

	got2, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(uint32(4), got2)
	s.Equal(uint32(4), s.h.PageCount)
}

func (s *FreeListSuite) TestFree_LIFOOrderAndHeadPointerStaysCorrect() {
	p1, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	p2, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	p3, err := Allocate(s.f, s.h)
	s.Require().NoError(err)

	s.Require().NoError(Free(s.f, s.h, p1))
	s.Equal(p1, s.h.FreeListHead)
	s.Require().NoError(Free(s.f, s.h, p2))
	s.Equal(p2, s.h.FreeListHead)
	s.Require().NoError(Free(s.f, s.h, p3))
	s.Equal(p3, s.h.FreeListHead)

	got, err := Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(p3, got)
	s.Equal(p2, s.h.FreeListHead)

	got, err = Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(p2, got)
	s.Equal(p1, s.h.FreeListHead)

	got, err = Allocate(s.f, s.h)
	s.Require().NoError(err)
	s.Equal(p1, got)
	s.Equal(uint32(0), s.h.FreeListHead)
}

func (s *FreeListSuite) TestFree_RejectsInvalidPageNumber() {
	p1, err := Allocate(s.f, s.h)
	s.Require().NoError(err)

	tests := []uint32{0, p1 + 1, math.MaxUint32}
	for _, pn := range tests {
		s.ErrorIs(Free(s.f, s.h, pn), ErrInvalidPageNumber)
	}
}

func (s *FreeListSuite) TestAllocate_PageCountOverflow() {
	s.h.PageCount = math.MaxUint32
	_, err := Allocate(s.f, s.h)
	s.ErrorIs(err, ErrPageCountOverflow)
}

func (s *FreeListSuite) TestAllocate_DetectsCorruptedFreeListHead() {
	p1, err := Allocate(s.f, s.h) // grown, blank content, never freed
	s.Require().NoError(err)

	s.h.FreeListHead = p1 // corrupt: point the free list at a non-free page
	_, err = Allocate(s.f, s.h)
	s.ErrorIs(err, ErrNotAFreePage)
}

func (s *FreeListSuite) TestAllocate_HeaderPersistedToDisk() {
	_, err := Allocate(s.f, s.h)
	s.Require().NoError(err)

	reread, err := ReadHeader(s.f)
	s.Require().NoError(err)
	s.Equal(s.h.PageCount, reread.PageCount)
	s.Equal(s.h.FreeListHead, reread.FreeListHead)
}
