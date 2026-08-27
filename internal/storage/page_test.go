package storage

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type PageSuite struct {
	suite.Suite
	f *os.File
}

func (s *PageSuite) SetupTest() {
	path := filepath.Join(s.T().TempDir(), "test.db")
	f, err := os.Create(path)
	s.Require().NoError(err)
	s.f = f
}

func (s *PageSuite) TearDownTest() {
	s.Require().NoError(s.f.Close())
}

func TestPage(t *testing.T) {
	suite.Run(t, new(PageSuite))
}

func (s *PageSuite) TestReadWritePage_RoundTrip() {
	pageSize := uint32(256)
	data := make([]byte, pageSize)
	for i := range data {
		data[i] = byte(i)
	}
	s.Require().NoError(WritePage(s.f, pageSize, 1, data))

	got, err := ReadPage(s.f, pageSize, 1)
	s.Require().NoError(err)
	s.Equal(data, got)
}

func (s *PageSuite) TestWritePage_GrowsFileWhenBeyondCurrentLength() {
	pageSize := uint32(128)
	s.Require().NoError(WritePage(s.f, pageSize, 5, make([]byte, pageSize)))

	info, err := s.f.Stat()
	s.Require().NoError(err)
	s.Equal(int64(6)*int64(pageSize), info.Size()) // pages 0..5
}

func (s *PageSuite) TestWritePage_RejectsWrongDataLength() {
	err := WritePage(s.f, 128, 1, make([]byte, 100))
	s.ErrorIs(err, ErrInvalidPageData)
}

func (s *PageSuite) TestReadPage_ErrorsOnShortFile() {
	_, err := ReadPage(s.f, 128, 1) // nothing written yet
	s.ErrorIs(err, ErrShortPageRead)
}

func (s *PageSuite) TestReadWritePage_RejectsInvalidPageSize() {
	tests := []uint32{0, 1, MinPageSize - 1, MaxPageSize + 1}
	for _, ps := range tests {
		s.ErrorIs(WritePage(s.f, ps, 1, make([]byte, ps)), ErrInvalidPageSize)
		_, err := ReadPage(s.f, ps, 1)
		s.ErrorIs(err, ErrInvalidPageSize)
	}
}

func (s *PageSuite) TestReadWriteHeader_RoundTrip() {
	h, err := NewHeader(256)
	s.Require().NoError(err)
	h.PageCount = 3
	h.FreeListHead = 2
	h.CatalogHead = 1
	h.Dirty = true

	s.Require().NoError(WriteHeader(s.f, h))

	got, err := ReadHeader(s.f)
	s.Require().NoError(err)
	s.Equal(h, got)
}

func (s *PageSuite) TestPageOffset_OverflowDetected() {
	_, err := pageOffset(math.MaxUint32, math.MaxUint32)
	s.ErrorIs(err, ErrPageOffsetOverflow)
}

func (s *PageSuite) TestPageOffset_ValidRange() {
	off, err := pageOffset(4096, 3)
	s.Require().NoError(err)
	s.Equal(int64(3*4096), off)
}
