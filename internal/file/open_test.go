//go:build unix

package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
)

type OpenSuite struct {
	suite.Suite
}

func TestOpen(t *testing.T) {
	suite.Run(t, new(OpenSuite))
}

func (s *OpenSuite) dir() string {
	return s.T().TempDir()
}

func (s *OpenSuite) TestOpenDB_CreatesFileWithDBModeAndExtension() {
	path := filepath.Join(s.dir(), "mydata")
	f, err := OpenDB(path)
	s.Require().NoError(err)
	defer f.Close()

	s.Equal(path+".db", f.Name())
	info, err := f.Stat()
	s.Require().NoError(err)
	s.Equal(dbFileMode, info.Mode().Perm())
}

func (s *OpenSuite) TestOpenDB_EmptyPathReturnsError() {
	_, err := OpenDB("")
	s.ErrorIs(err, ErrEmptyDatabasePath)
}

func (s *OpenSuite) TestCreateJournal_CreatesSiblingWithJournalMode() {
	dbPath := filepath.Join(s.dir(), "mydata.db")
	dbFile, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, dbFileMode)
	s.Require().NoError(err)
	defer dbFile.Close()

	jf, err := CreateJournal(dbFile)
	s.Require().NoError(err)
	defer jf.Close()

	s.Equal(dbPath+".journal", jf.Name())
	info, err := jf.Stat()
	s.Require().NoError(err)
	s.Equal(journalFileMode, info.Mode().Perm())
}

func (s *OpenSuite) TestCreateJournal_TruncatesStaleLeftover() {
	dbPath := filepath.Join(s.dir(), "mydata.db")
	dbFile, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, dbFileMode)
	s.Require().NoError(err)
	defer dbFile.Close()

	stale, err := CreateJournal(dbFile)
	s.Require().NoError(err)
	_, err = stale.Write([]byte("leftover from a previous cycle"))
	s.Require().NoError(err)
	s.Require().NoError(stale.Close())

	jf, err := CreateJournal(dbFile)
	s.Require().NoError(err)
	defer jf.Close()

	info, err := jf.Stat()
	s.Require().NoError(err)
	s.Equal(int64(0), info.Size())
}

func (s *OpenSuite) TestOpenJournalReadOnly_FailsWhenMissing() {
	dbPath := filepath.Join(s.dir(), "mydata.db")
	dbFile, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, dbFileMode)
	s.Require().NoError(err)
	defer dbFile.Close()

	_, err = OpenJournalReadOnly(dbFile)
	s.True(os.IsNotExist(err))
}

func (s *OpenSuite) TestOpenJournalReadOnly_OpensExisting() {
	dbPath := filepath.Join(s.dir(), "mydata.db")
	dbFile, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, dbFileMode)
	s.Require().NoError(err)
	defer dbFile.Close()

	created, err := CreateJournal(dbFile)
	s.Require().NoError(err)
	s.Require().NoError(created.Close())

	jf, err := OpenJournalReadOnly(dbFile)
	s.Require().NoError(err)
	defer jf.Close()
	s.Equal(dbPath+".journal", jf.Name())
}
