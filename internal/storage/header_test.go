package storage

import (
	"math"
	"testing"

	"github.com/stretchr/testify/suite"
)

type HeaderSuite struct {
	suite.Suite
}

func TestHeader(t *testing.T) {
	suite.Run(t, new(HeaderSuite))
}

func (s *HeaderSuite) TestNewHeader_DefaultsPageSizeWhenZero() {
	h, err := NewHeader(0)
	s.Require().NoError(err)
	s.Equal(DefaultPageSize, h.PageSize)
	s.Equal(FormatVersion, h.Version)
}

func (s *HeaderSuite) TestNewHeader_RejectsPageSizeOutOfRange() {
	tests := []uint32{1, MinPageSize - 1, MaxPageSize + 1}
	for _, ps := range tests {
		_, err := NewHeader(ps)
		s.ErrorIs(err, ErrInvalidPageSize)
	}
}

func (s *HeaderSuite) TestEncodeDecode_RoundTrip() {
	tests := []struct {
		name string
		h    Header
	}{
		{"zero values", Header{Version: FormatVersion, PageSize: MinPageSize}},
		{"typical", Header{Version: FormatVersion, PageSize: 4096, PageCount: 10, FreeListHead: 3, CatalogHead: 1}},
		{"dirty flag set", Header{Version: FormatVersion, PageSize: 4096, PageCount: 1, Dirty: true}},
		{
			"max field values",
			Header{
				Version:      FormatVersion,
				PageSize:     MaxPageSize,
				PageCount:    math.MaxUint32,
				FreeListHead: math.MaxUint32,
				CatalogHead:  math.MaxUint32,
				Dirty:        true,
			},
		},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			buf := tt.h.Encode()
			s.Len(buf, HeaderSize)
			got, err := DecodeHeader(buf)
			s.Require().NoError(err)
			s.Equal(&tt.h, got)
		})
	}
}

func (s *HeaderSuite) TestDecodeHeader_RejectsShortBuffer() {
	tests := []int{0, 1, HeaderSize - 1}
	for _, n := range tests {
		_, err := DecodeHeader(make([]byte, n))
		s.ErrorIs(err, ErrTruncatedHeader)
	}
}

func (s *HeaderSuite) TestDecodeHeader_RejectsBadMagic() {
	h := Header{Version: FormatVersion, PageSize: DefaultPageSize}
	buf := h.Encode()
	buf[0] = 'X'
	_, err := DecodeHeader(buf)
	s.ErrorIs(err, ErrNotAnAtlasFile)
}

func (s *HeaderSuite) TestDecodeHeader_DetectsChecksumCorruption() {
	h := Header{Version: FormatVersion, PageSize: DefaultPageSize, PageCount: 5}
	buf := h.Encode()
	buf[16] ^= 0xFF // flip a byte inside the checksummed PageCount field
	_, err := DecodeHeader(buf)
	s.ErrorIs(err, ErrCorruptedHeader)
}

func (s *HeaderSuite) TestDecodeHeader_RejectsIncompatibleVersion() {
	h := Header{Version: FormatVersion + 1, PageSize: DefaultPageSize}
	buf := h.Encode() // checksum computed over the wrong version, so it's internally consistent
	_, err := DecodeHeader(buf)
	s.ErrorIs(err, ErrIncompatibleVersion)
}

func (s *HeaderSuite) TestDecodeHeader_RejectsCorruptedPageSize() {
	tests := []uint32{0, MinPageSize - 1, MaxPageSize + 1}
	for _, ps := range tests {
		h := Header{Version: FormatVersion, PageSize: ps}
		buf := h.Encode()
		_, err := DecodeHeader(buf)
		s.ErrorIs(err, ErrCorruptedHeader)
	}
}

func (s *HeaderSuite) TestValidatePageCount() {
	h := Header{PageSize: 100, PageCount: 4}
	s.NoError(h.ValidatePageCount(500)) // (4+1)*100
	s.ErrorIs(h.ValidatePageCount(499), ErrCorruptedHeader)
	s.ErrorIs(h.ValidatePageCount(-1), ErrCorruptedHeader)
}
