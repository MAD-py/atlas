package storage

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type CatalogSlotSuite struct {
	suite.Suite
}

func TestCatalogSlot(t *testing.T) {
	suite.Run(t, new(CatalogSlotSuite))
}

func (s *CatalogSlotSuite) TestEncodeDecode_RoundTrip() {
	tests := []struct {
		name string
		slot CollectionSlot
	}{
		{"zero value", CollectionSlot{}},
		{"typical user collection", CollectionSlot{Name: "users", Head: 3, Tail: 7, PageCount: 2, DocCount: 100}},
		{"internal collection", CollectionSlot{Name: "_catalog_meta", Flags: slotFlagInternal, Head: 1, Tail: 1, PageCount: 1}},
		{"tombstoned", CollectionSlot{Name: "old", Flags: slotFlagTombstone}},
		{"both flags", CollectionSlot{Name: "both", Flags: slotFlagTombstone | slotFlagInternal}},
		{"max uint32 fields", CollectionSlot{Name: "big", Head: 1<<32 - 1, Tail: 1<<32 - 1, PageCount: 1<<32 - 1, DocCount: 1<<32 - 1}},
		{"exactly 64-byte name", CollectionSlot{Name: strings.Repeat("a", 64)}},
		{"unicode name", CollectionSlot{Name: "café☕"}},
	}
	for _, tt := range tests {
		s.Run(tt.name, func() {
			buf, err := tt.slot.Encode()
			s.Require().NoError(err)
			s.Len(buf, collectionSlotSize)

			got, err := DecodeCollectionSlot(buf)
			s.Require().NoError(err)
			s.Equal(tt.slot, got)
		})
	}
}

func (s *CatalogSlotSuite) TestEncode_RejectsNameOver64Bytes() {
	slot := CollectionSlot{Name: strings.Repeat("a", 65)}
	_, err := slot.Encode()
	s.ErrorIs(err, ErrCollectionNameTooLong)
}

func (s *CatalogSlotSuite) TestEncode_Accepts64ByteNameExactly() {
	slot := CollectionSlot{Name: strings.Repeat("a", 64)}
	_, err := slot.Encode()
	s.NoError(err)
}

func (s *CatalogSlotSuite) TestEncode_RejectsEmbeddedNulByte() {
	slot := CollectionSlot{Name: "bad\x00name"}
	_, err := slot.Encode()
	s.ErrorIs(err, ErrInvalidCollectionName)
}

func (s *CatalogSlotSuite) TestDecodeCollectionSlot_DetectsChecksumCorruption() {
	slot := CollectionSlot{Name: "users", Head: 3, Tail: 7}
	buf, err := slot.Encode()
	s.Require().NoError(err)

	buf[10] ^= 0xFF // flip a byte inside the name field

	_, err = DecodeCollectionSlot(buf)
	s.ErrorIs(err, ErrCorruptedCollectionSlot)
}

func (s *CatalogSlotSuite) TestDecodeCollectionSlot_DetectsCorruptionInEveryField() {
	slot := CollectionSlot{Name: "users", Head: 3, Tail: 7, PageCount: 1, DocCount: 2}
	buf, err := slot.Encode()
	s.Require().NoError(err)

	for i := range buf[:81] { // every byte outside the checksum itself
		corrupted := append([]byte(nil), buf...)
		corrupted[i] ^= 0xFF
		_, err := DecodeCollectionSlot(corrupted)
		s.ErrorIsf(err, ErrCorruptedCollectionSlot, "byte %d", i)
	}
}

func (s *CatalogSlotSuite) TestDecodeCollectionSlot_RejectsWrongLength() {
	tests := []int{0, 1, collectionSlotSize - 1, collectionSlotSize + 1}
	for _, n := range tests {
		_, err := DecodeCollectionSlot(make([]byte, n))
		s.ErrorIs(err, ErrShortPageRead)
	}
}

func (s *CatalogSlotSuite) TestFlags_IsTombstoneIsInternal() {
	s.False(CollectionSlot{}.IsTombstone())
	s.False(CollectionSlot{}.IsInternal())
	s.True(CollectionSlot{Flags: slotFlagTombstone}.IsTombstone())
	s.False(CollectionSlot{Flags: slotFlagTombstone}.IsInternal())
	s.True(CollectionSlot{Flags: slotFlagInternal}.IsInternal())
	s.True(CollectionSlot{Flags: slotFlagTombstone | slotFlagInternal}.IsTombstone())
	s.True(CollectionSlot{Flags: slotFlagTombstone | slotFlagInternal}.IsInternal())
}

func (s *CatalogSlotSuite) TestValidateCollectionName() {
	s.NoError(validateCollectionName(""))
	s.NoError(validateCollectionName(strings.Repeat("a", 64)))
	s.ErrorIs(validateCollectionName(strings.Repeat("a", 65)), ErrCollectionNameTooLong)
	s.ErrorIs(validateCollectionName("bad\x00name"), ErrInvalidCollectionName)
}
