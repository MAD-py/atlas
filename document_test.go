package atlas

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

type DocumentSuite struct {
	suite.Suite
}

func TestDocument(t *testing.T) {
	suite.Run(t, new(DocumentSuite))
}

func (s *DocumentSuite) TestNewAtlasID_TimestampMatchesNow() {
	before := time.Now().Unix()
	id, err := NewAtlasID()
	after := time.Now().Unix()
	s.Require().NoError(err)

	got := int64(binary.BigEndian.Uint32(id[0:4]))
	s.GreaterOrEqual(got, before)
	s.LessOrEqual(got, after)
}

func (s *DocumentSuite) TestNewAtlasID_CounterIncrementsMonotonically() {
	first, err := NewAtlasID()
	s.Require().NoError(err)
	second, err := NewAtlasID()
	s.Require().NoError(err)

	firstCounter := binary.BigEndian.Uint32(first[8:12])
	secondCounter := binary.BigEndian.Uint32(second[8:12])
	s.Less(firstCounter, secondCounter)
}

func (s *DocumentSuite) TestAtlasID_StringIsLowercase24CharHex() {
	id, err := NewAtlasID()
	s.Require().NoError(err)
	str := id.String()
	s.Len(str, 24)
	s.Regexp("^[0-9a-f]{24}$", str)
}

func (s *DocumentSuite) TestParseAtlasID_RoundTripsWithString() {
	id, err := NewAtlasID()
	s.Require().NoError(err)

	got, err := ParseAtlasID(id.String())
	s.Require().NoError(err)
	s.Equal(id, got)
}

func (s *DocumentSuite) TestAtlasID_MarshalTextUnmarshalTextRoundTrip() {
	id, err := NewAtlasID()
	s.Require().NoError(err)

	text, err := id.MarshalText()
	s.Require().NoError(err)

	var got AtlasID
	s.Require().NoError(got.UnmarshalText(text))
	s.Equal(id, got)
}

func (s *DocumentSuite) TestParseAtlasID_RejectsWrongByteLength() {
	_, err := ParseAtlasID("abcd")
	s.ErrorIs(err, ErrInvalidAtlasID)
}

func (s *DocumentSuite) TestParseAtlasID_RejectsInvalidHex() {
	_, err := ParseAtlasID(strings.Repeat("z", 24))
	s.ErrorIs(err, ErrInvalidAtlasID)
}

func (s *DocumentSuite) TestDocument_JSONMarshalsIDAsHexString() {
	id, err := NewAtlasID()
	s.Require().NoError(err)
	doc := Document{ID: id, Fields: map[string]any{"name": "ada"}}

	data, err := json.Marshal(doc)
	s.Require().NoError(err)

	var decoded map[string]any
	s.Require().NoError(json.Unmarshal(data, &decoded))
	s.Equal(id.String(), decoded["ID"])
}

func (s *DocumentSuite) TestNewAtlasID_UniqueAcrossManyCalls() {
	const n = 1000
	seen := make(map[AtlasID]bool, n)
	for range n {
		id, err := NewAtlasID()
		s.Require().NoError(err)
		s.False(seen[id], "duplicate AtlasID generated")
		seen[id] = true
	}
}
