package atlas

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/MAD-py/atlas/internal/storage"
)

type AtlasSuite struct {
	suite.Suite
}

func TestAtlas(t *testing.T) {
	suite.Run(t, new(AtlasSuite))
}

func (s *AtlasSuite) path() string {
	return filepath.Join(s.T().TempDir(), "test.db")
}

// --- Open / Close ---

func (s *AtlasSuite) TestOpen_BootstrapsNewFile() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	col, err := db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)
	s.NotNil(col)
}

func (s *AtlasSuite) TestOpen_AppendsDBExtensionWhenMissing() {
	ctx := context.Background()
	path := filepath.Join(s.T().TempDir(), "mydata")
	db, err := Open(ctx, path)
	s.Require().NoError(err)
	defer db.Close()

	_, statErr := os.Stat(path + ".db")
	s.NoError(statErr)
}

func (s *AtlasSuite) TestOpen_EmptyPathFailsWithErrEmptyDatabasePath() {
	_, err := Open(context.Background(), "")
	s.ErrorIs(err, ErrEmptyDatabasePath)
}

func (s *AtlasSuite) TestOpen_RecoversExistingFile() {
	ctx := context.Background()
	path := s.path()

	db, err := Open(ctx, path)
	s.Require().NoError(err)
	_, err = db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)
	s.Require().NoError(db.Close())

	db2, err := Open(ctx, path)
	s.Require().NoError(err)
	defer db2.Close()

	_, err = db2.Collection(ctx, "docs")
	s.Require().NoError(err)
}

func (s *AtlasSuite) TestOpen_SecondConnectionFailsWithErrLocked_ThenThirdSucceedsAfterClose() {
	ctx := context.Background()
	path := s.path()

	db1, err := Open(ctx, path)
	s.Require().NoError(err)

	_, err = Open(ctx, path)
	s.ErrorIs(err, ErrLocked)

	s.Require().NoError(db1.Close())

	db3, err := Open(ctx, path)
	s.Require().NoError(err)
	s.Require().NoError(db3.Close())
}

func (s *AtlasSuite) TestClose_Idempotent() {
	db, err := Open(context.Background(), s.path())
	s.Require().NoError(err)
	s.Require().NoError(db.Close())
	s.Require().NoError(db.Close())
}

func (s *AtlasSuite) TestMethodsAfterClose_ReturnErrClosed() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	s.Require().NoError(db.Close())

	_, err = db.CreateCollection(ctx, "docs")
	s.ErrorIs(err, ErrClosed)

	_, err = db.Collection(ctx, "docs")
	s.ErrorIs(err, ErrClosed)

	err = db.DropCollection(ctx, "docs")
	s.ErrorIs(err, ErrClosed)
}

// --- CreateCollection / Collection / DropCollection ---

func (s *AtlasSuite) TestCreateCollection_AlreadyExists() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	_, err = db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)

	_, err = db.CreateCollection(ctx, "docs")
	s.ErrorIs(err, ErrCollectionAlreadyExists)
}

func (s *AtlasSuite) TestCreateCollection_NameTooLong() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	longName := strings.Repeat("a", storage.MaxCollectionNameLen+1)
	_, err = db.CreateCollection(ctx, longName)
	s.ErrorIs(err, ErrCollectionNameTooLong)
}

func (s *AtlasSuite) TestCollection_NotFound() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	_, err = db.Collection(ctx, "ghost")
	s.ErrorIs(err, ErrCollectionNotFound)
}

func (s *AtlasSuite) TestDropCollection_Success() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	_, err = db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)

	s.Require().NoError(db.DropCollection(ctx, "docs"))

	_, err = db.Collection(ctx, "docs")
	s.ErrorIs(err, ErrCollectionNotFound)
}

func (s *AtlasSuite) TestDropCollection_NotFound() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	err = db.DropCollection(ctx, "ghost")
	s.ErrorIs(err, ErrCollectionNotFound)
}

// TestDropCollection_FreesDataPages is the reason FreeCollectionDataPages
// exists: dropping a non-empty collection must return its data pages to the
// free-list, not just leak them. Collection.Insert doesn't exist yet, so
// this reaches into internal/storage directly (same module) to populate the
// collection created through the public API.
func (s *AtlasSuite) TestDropCollection_FreesDataPages() {
	ctx := context.Background()
	db, err := Open(ctx, s.path())
	s.Require().NoError(err)
	defer db.Close()

	_, err = db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)

	ref, slot, err := storage.FindCollectionSlot(ctx, db.f, db.h, "docs")
	s.Require().NoError(err)

	cycle, err := storage.NewJournalCycle(ctx, db.f, db.h)
	s.Require().NoError(err)
	for i := byte(1); i <= 5; i++ {
		record := make([]byte, AtlasIDSize+4)
		record[0] = i
		copy(record[AtlasIDSize:], []byte{0xDE, 0xAD, 0xBE, 0xEF})
		_, _, err := storage.InsertRecord(ctx, db.f, db.h, cycle, ref, &slot, record)
		s.Require().NoError(err)
	}
	s.Require().NoError(cycle.Commit(ctx, db.f, db.h))
	s.Require().Greater(slot.PageCount, uint32(0))
	freedHead := slot.Head

	s.Require().NoError(db.DropCollection(ctx, "docs"))

	// The freed page must now be reachable via the free-list: allocating
	// hands it straight back instead of growing the file.
	cycle2, err := storage.NewJournalCycle(ctx, db.f, db.h)
	s.Require().NoError(err)
	allocated, err := storage.Allocate(ctx, db.f, db.h, cycle2)
	s.Require().NoError(err)
	s.Require().NoError(cycle2.Commit(ctx, db.f, db.h))

	s.Equal(freedHead, allocated)
}
