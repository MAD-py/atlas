package file

import (
	"testing"

	"github.com/stretchr/testify/suite"
)

type PathSuite struct {
	suite.Suite
}

func TestPath(t *testing.T) {
	suite.Run(t, new(PathSuite))
}

func (s *PathSuite) TestDBPathFor_AppendsExtensionWhenMissing() {
	got, err := DBPathFor("mydata")
	s.Require().NoError(err)
	s.Equal("mydata.db", got)
}

func (s *PathSuite) TestDBPathFor_LeavesExtensionWhenPresent() {
	got, err := DBPathFor("mydata.db")
	s.Require().NoError(err)
	s.Equal("mydata.db", got)
}

func (s *PathSuite) TestDBPathFor_PreservesDirectoryComponent() {
	got, err := DBPathFor("some/dir/mydata")
	s.Require().NoError(err)
	s.Equal("some/dir/mydata.db", got)
}

func (s *PathSuite) TestDBPathFor_EmptyPathReturnsError() {
	_, err := DBPathFor("")
	s.ErrorIs(err, ErrEmptyDatabasePath)
}

func (s *PathSuite) TestJournalPathFor_AppendsJournalExtension() {
	s.Equal("mydata.db.journal", JournalPathFor("mydata.db"))
}
