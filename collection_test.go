package atlas

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// tinyPageSize mirrors internal/storage's own small-page test constant: big
// enough for the catalog to bootstrap one slot per page, small enough that
// the per-document size limit (page minus data-page header minus one slot)
// is reachable with a single short string field.
const tinyPageSize = 92

type CollectionSuite struct {
	suite.Suite
}

func TestCollection(t *testing.T) {
	suite.Run(t, new(CollectionSuite))
}

func (s *CollectionSuite) path() string {
	return filepath.Join(s.T().TempDir(), "test.db")
}

// newCollection opens a fresh database and creates "docs" in it, returning
// both so the caller can still reach DB-level operations (Close, Drop).
func (s *CollectionSuite) newCollection(ctx context.Context, opts ...Option) (*DB, *Collection) {
	db, err := Open(ctx, s.path(), opts...)
	s.Require().NoError(err)
	s.T().Cleanup(func() { db.Close() })

	col, err := db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)
	return db, col
}

// --- Insert / FindByID ---

func (s *CollectionSuite) TestInsertThenFindByID_RoundTrips() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	fields := map[string]any{
		"name":     "ada",
		"age":      int64(36),
		"score":    1.5,
		"active":   true,
		"tags":     []any{"a", "b"},
		"note":     nil,
		"birthday": NewDate(1815, time.December, 10),
	}

	id, err := col.Insert(ctx, Document{Fields: fields})
	s.Require().NoError(err)
	s.NotEqual(AtlasID{}, id)

	doc, err := col.FindByID(ctx, id)
	s.Require().NoError(err)
	s.Equal(id, doc.ID)
	s.Equal(fields, doc.Fields)
}

func (s *CollectionSuite) TestInsert_IgnoresCallerSuppliedID() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	supplied := AtlasID{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}
	id, err := col.Insert(ctx, Document{ID: supplied, Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)
	s.NotEqual(supplied, id)

	_, err = col.FindByID(ctx, supplied)
	s.ErrorIs(err, ErrDocumentNotFound)

	doc, err := col.FindByID(ctx, id)
	s.Require().NoError(err)
	s.Equal(id, doc.ID)
}

func (s *CollectionSuite) TestInsert_MultipleDocumentsGetDistinctIDs() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	seen := make(map[AtlasID]struct{})
	for i := range 10 {
		id, err := col.Insert(ctx, Document{Fields: map[string]any{"i": int64(i)}})
		s.Require().NoError(err)
		s.NotContains(seen, id)
		seen[id] = struct{}{}
	}
}

func (s *CollectionSuite) TestFindByID_MissingDocument() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	missing, err := NewAtlasID()
	s.Require().NoError(err)

	_, err = col.FindByID(ctx, missing)
	s.ErrorIs(err, ErrDocumentNotFound)

	// Same result once the collection has pages, not just while it's empty.
	_, err = col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)

	_, err = col.FindByID(ctx, missing)
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *CollectionSuite) TestInsert_DocumentTooLarge() {
	ctx := context.Background()
	_, col := s.newCollection(ctx, WithPageSize(tinyPageSize))

	_, err := col.Insert(ctx, Document{Fields: map[string]any{"big": strings.Repeat("x", tinyPageSize)}})
	s.ErrorIs(err, ErrDocumentTooLarge)
}

// --- Delete ---

func (s *CollectionSuite) TestDelete_ThenFindByIDReturnsNotFound() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	id, err := col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)

	s.Require().NoError(col.Delete(ctx, id))

	_, err = col.FindByID(ctx, id)
	s.ErrorIs(err, ErrDocumentNotFound)
}

func (s *CollectionSuite) TestDelete_MissingDocument() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	missing, err := NewAtlasID()
	s.Require().NoError(err)

	s.ErrorIs(col.Delete(ctx, missing), ErrDocumentNotFound)
}

func (s *CollectionSuite) TestDelete_LeavesOtherDocumentsReadable() {
	ctx := context.Background()
	_, col := s.newCollection(ctx, WithPageSize(tinyPageSize))

	var ids []AtlasID
	for i := range 6 {
		id, err := col.Insert(ctx, Document{Fields: map[string]any{"i": int64(i)}})
		s.Require().NoError(err)
		ids = append(ids, id)
	}

	s.Require().NoError(col.Delete(ctx, ids[0]))
	s.Require().NoError(col.Delete(ctx, ids[3]))

	for i, id := range ids {
		doc, err := col.FindByID(ctx, id)
		if i == 0 || i == 3 {
			s.ErrorIs(err, ErrDocumentNotFound)
			continue
		}
		s.Require().NoError(err)
		s.Equal(map[string]any{"i": int64(i)}, doc.Fields)
	}
}

// --- Update / Replace ---

func (s *CollectionSuite) TestUpdate_AppliesChanges() {
	tests := []struct {
		name    string
		initial map[string]any
		changes Update
		want    map[string]any
	}{
		{
			name:    "adds a new field",
			initial: map[string]any{"name": "ada"},
			changes: Update{Set: map[string]any{"age": int64(36)}},
			want:    map[string]any{"name": "ada", "age": int64(36)},
		},
		{
			name:    "overwrites an existing field",
			initial: map[string]any{"name": "ada", "age": int64(36)},
			changes: Update{Set: map[string]any{"age": int64(37)}},
			want:    map[string]any{"name": "ada", "age": int64(37)},
		},
		{
			name:    "removes a field",
			initial: map[string]any{"name": "ada", "age": int64(36)},
			changes: Update{Unset: []string{"age"}},
			want:    map[string]any{"name": "ada"},
		},
		{
			name:    "unsetting an absent field is a no-op",
			initial: map[string]any{"name": "ada"},
			changes: Update{Unset: []string{"ghost"}},
			want:    map[string]any{"name": "ada"},
		},
		{
			name:    "a key in both Set and Unset ends up set",
			initial: map[string]any{"name": "ada", "age": int64(36)},
			changes: Update{Set: map[string]any{"age": int64(37)}, Unset: []string{"age"}},
			want:    map[string]any{"name": "ada", "age": int64(37)},
		},
		{
			name:    "sets and unsets in the same call",
			initial: map[string]any{"name": "ada", "age": int64(36)},
			changes: Update{Set: map[string]any{"city": "london"}, Unset: []string{"age"}},
			want:    map[string]any{"name": "ada", "city": "london"},
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			ctx := context.Background()
			_, col := s.newCollection(ctx)

			id, err := col.Insert(ctx, Document{Fields: tt.initial})
			s.Require().NoError(err)

			s.Require().NoError(col.Update(ctx, id, tt.changes))

			doc, err := col.FindByID(ctx, id)
			s.Require().NoError(err)
			s.Equal(id, doc.ID)
			s.Equal(tt.want, doc.Fields)
		})
	}
}

func (s *CollectionSuite) TestUpdate_KeepsCollectionCount() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	id, err := col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)

	s.Require().NoError(col.Update(ctx, id, Update{Set: map[string]any{"k": "w"}}))

	n, err := col.Count(ctx)
	s.Require().NoError(err)
	s.Equal(1, n)
}

func (s *CollectionSuite) TestReplace_WipesFieldsAbsentFromTheNewMap() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	id, err := col.Insert(ctx, Document{Fields: map[string]any{"name": "ada", "age": int64(36), "city": "london"}})
	s.Require().NoError(err)

	s.Require().NoError(col.Replace(ctx, id, map[string]any{"age": int64(31)}))

	doc, err := col.FindByID(ctx, id)
	s.Require().NoError(err)
	s.Equal(id, doc.ID)
	s.Equal(map[string]any{"age": int64(31)}, doc.Fields)
}

func (s *CollectionSuite) TestUpdateAndReplace_MissingDocument() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	missing, err := NewAtlasID()
	s.Require().NoError(err)

	s.ErrorIs(col.Update(ctx, missing, Update{Set: map[string]any{"k": "v"}}), ErrDocumentNotFound)
	s.ErrorIs(col.Replace(ctx, missing, map[string]any{"k": "v"}), ErrDocumentNotFound)

	// Same result once the collection actually has pages to scan.
	_, err = col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)

	s.ErrorIs(col.Update(ctx, missing, Update{Set: map[string]any{"k": "v"}}), ErrDocumentNotFound)
	s.ErrorIs(col.Replace(ctx, missing, map[string]any{"k": "v"}), ErrDocumentNotFound)
}

func (s *CollectionSuite) TestReplace_DocumentTooLargeLeavesTheOriginalIntact() {
	ctx := context.Background()
	_, col := s.newCollection(ctx, WithPageSize(tinyPageSize))

	id, err := col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)

	oversized := map[string]any{"big": strings.Repeat("x", tinyPageSize)}
	s.ErrorIs(col.Replace(ctx, id, oversized), ErrDocumentTooLarge)
	s.ErrorIs(col.Update(ctx, id, Update{Set: oversized}), ErrDocumentTooLarge)

	doc, err := col.FindByID(ctx, id)
	s.Require().NoError(err)
	s.Equal(map[string]any{"k": "v"}, doc.Fields)
}

// --- Count ---

func (s *CollectionSuite) TestCount_TracksInsertsAndDeletes() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	n, err := col.Count(ctx)
	s.Require().NoError(err)
	s.Equal(0, n)

	var ids []AtlasID
	for i := range 3 {
		id, err := col.Insert(ctx, Document{Fields: map[string]any{"i": int64(i)}})
		s.Require().NoError(err)
		ids = append(ids, id)

		n, err = col.Count(ctx)
		s.Require().NoError(err)
		s.Equal(i+1, n)
	}

	for i, id := range ids {
		s.Require().NoError(col.Delete(ctx, id))

		n, err = col.Count(ctx)
		s.Require().NoError(err)
		s.Equal(len(ids)-i-1, n)
	}
}

func (s *CollectionSuite) TestCount_SurvivesReopen() {
	ctx := context.Background()
	path := s.path()

	db, err := Open(ctx, path)
	s.Require().NoError(err)
	col, err := db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)
	for i := range 4 {
		_, err := col.Insert(ctx, Document{Fields: map[string]any{"i": int64(i)}})
		s.Require().NoError(err)
	}
	s.Require().NoError(db.Close())

	db2, err := Open(ctx, path)
	s.Require().NoError(err)
	defer db2.Close()

	col2, err := db2.Collection(ctx, "docs")
	s.Require().NoError(err)

	n, err := col2.Count(ctx)
	s.Require().NoError(err)
	s.Equal(4, n)
}

// --- Stale handles ---

// A *Collection caches nothing, so every method on a handle whose collection
// was dropped underneath it must fail rather than operate on a stale slot.
func (s *CollectionSuite) TestMethodsAfterDropCollection_ReturnErrCollectionNotFound() {
	ctx := context.Background()
	db, col := s.newCollection(ctx)

	id, err := col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)

	s.Require().NoError(db.DropCollection(ctx, "docs"))

	_, err = col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.ErrorIs(err, ErrCollectionNotFound)

	_, err = col.FindByID(ctx, id)
	s.ErrorIs(err, ErrCollectionNotFound)

	s.ErrorIs(col.Delete(ctx, id), ErrCollectionNotFound)

	s.ErrorIs(col.Update(ctx, id, Update{Set: map[string]any{"k": "w"}}), ErrCollectionNotFound)

	s.ErrorIs(col.Replace(ctx, id, map[string]any{"k": "w"}), ErrCollectionNotFound)

	_, err = col.Count(ctx)
	s.ErrorIs(err, ErrCollectionNotFound)
}

func (s *CollectionSuite) TestMethodsAfterClose_ReturnErrClosed() {
	ctx := context.Background()
	db, col := s.newCollection(ctx)

	id, err := col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.Require().NoError(err)
	s.Require().NoError(db.Close())

	_, err = col.Insert(ctx, Document{Fields: map[string]any{"k": "v"}})
	s.ErrorIs(err, ErrClosed)

	_, err = col.FindByID(ctx, id)
	s.ErrorIs(err, ErrClosed)

	s.ErrorIs(col.Delete(ctx, id), ErrClosed)

	s.ErrorIs(col.Update(ctx, id, Update{Set: map[string]any{"k": "w"}}), ErrClosed)

	s.ErrorIs(col.Replace(ctx, id, map[string]any{"k": "w"}), ErrClosed)

	_, err = col.Count(ctx)
	s.ErrorIs(err, ErrClosed)
}
