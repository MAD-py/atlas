package atlas

import (
	"encoding/binary"
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

func (s *DocumentSuite) TestNewAtlasID_UniqueAcrossManyCalls() {
	const n = 1000
	seen := make(map[AtlasID]bool, n)
	for i := 0; i < n; i++ {
		id, err := NewAtlasID()
		s.Require().NoError(err)
		s.False(seen[id], "duplicate AtlasID generated")
		seen[id] = true
	}
}
