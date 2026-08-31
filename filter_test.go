package atlas

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/MAD-py/atlas/internal/storage"
)

type FilterSuite struct {
	suite.Suite
}

func TestFilter(t *testing.T) {
	suite.Run(t, new(FilterSuite))
}

func (s *FilterSuite) path() string {
	return filepath.Join(s.T().TempDir(), "test.db")
}

func (s *FilterSuite) newCollection(ctx context.Context, opts ...Option) (*DB, *Collection) {
	db, err := Open(ctx, s.path(), opts...)
	s.Require().NoError(err)
	s.T().Cleanup(func() { db.Close() })

	col, err := db.CreateCollection(ctx, "docs")
	s.Require().NoError(err)
	return db, col
}

func (s *FilterSuite) insert(ctx context.Context, col *Collection, fields map[string]any) AtlasID {
	id, err := col.Insert(ctx, Document{Fields: fields})
	s.Require().NoError(err)
	return id
}

// numberedDocs fills a collection with {"n": 0..count-1} on a tiny page size,
// asserting the result really spans several pages so scans that must cross a
// page boundary aren't silently reduced to single-page ones.
func (s *FilterSuite) numberedDocs(ctx context.Context, count int) (*DB, *Collection) {
	db, col := s.newCollection(ctx, WithPageSize(tinyPageSize))
	for i := range count {
		s.insert(ctx, col, map[string]any{"n": int64(i)})
	}

	_, slot, err := storage.FindCollectionSlot(ctx, db.f, db.h, "docs")
	s.Require().NoError(err)
	s.Require().Greater(slot.PageCount, uint32(1))
	return db, col
}

func (s *FilterSuite) collect(cur *Cursor) []Document {
	var docs []Document
	for cur.Next() {
		doc, err := cur.Document()
		s.Require().NoError(err)
		docs = append(docs, doc)
	}
	s.Require().NoError(cur.Err())
	s.Require().NoError(cur.Close())
	return docs
}

func (s *FilterSuite) find(ctx context.Context, col *Collection, filter Filter) []Document {
	cur, err := col.Find(ctx, filter)
	s.Require().NoError(err)
	return s.collect(cur)
}

func (s *FilterSuite) values(docs []Document, field string) []any {
	var out []any
	for _, doc := range docs {
		out = append(out, doc.Fields[field])
	}
	return out
}

// --- builders ---

func (s *FilterSuite) TestBuilders_ProduceTheMatchingOperator() {
	s.Equal(Filter{field: "a", op: opEq, value: 1}, Eq("a", 1))
	s.Equal(Filter{field: "a", op: opGt, value: 1}, Gt("a", 1))
	s.Equal(Filter{field: "a", op: opLt, value: 1}, Lt("a", 1))
	s.Equal(Filter{field: "a", op: opGte, value: 1}, Gte("a", 1))
	s.Equal(Filter{field: "a", op: opLte, value: 1}, Lte("a", 1))
}

// --- matching ---

// A stored integer comes back from the decoder as int64, while a filter
// written as Eq("age", 30) carries a plain int: the two must still match.
func (s *FilterSuite) TestFind_EqMatchesStoredInt64AgainstUntypedIntLiteral() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	id := s.insert(ctx, col, map[string]any{"age": int64(30)})
	s.insert(ctx, col, map[string]any{"age": int64(31)})

	docs := s.find(ctx, col, Eq("age", 30))
	s.Require().Len(docs, 1)
	s.Equal(id, docs[0].ID)
	s.Equal(int64(30), docs[0].Fields["age"])
}

func (s *FilterSuite) TestFind_NumericComparisonIgnoresConcreteType() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	s.insert(ctx, col, map[string]any{"age": int64(30), "score": 1.5})

	tests := []struct {
		name   string
		filter Filter
		want   bool
	}{
		{name: "int literal against stored int64", filter: Eq("age", 30), want: true},
		{name: "int32 against stored int64", filter: Eq("age", int32(30)), want: true},
		{name: "uint against stored int64", filter: Eq("age", uint(30)), want: true},
		{name: "float64 against stored int64", filter: Eq("age", 30.0), want: true},
		{name: "float32 against stored float64", filter: Eq("score", float32(1.5)), want: true},
		{name: "int against stored float64", filter: Gt("score", 1), want: true},
		{name: "wrong number never matches", filter: Eq("age", 31), want: false},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			docs := s.find(ctx, col, tt.filter)
			if tt.want {
				s.Len(docs, 1)
				return
			}
			s.Empty(docs)
		})
	}
}

func (s *FilterSuite) TestFind_ComparisonOperators() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	for _, age := range []int64{10, 20, 30} {
		s.insert(ctx, col, map[string]any{"age": age})
	}
	s.insert(ctx, col, map[string]any{"name": "ageless"})

	tests := []struct {
		name   string
		filter Filter
		want   []any
	}{
		{name: "eq", filter: Eq("age", 20), want: []any{int64(20)}},
		{name: "gt", filter: Gt("age", 20), want: []any{int64(30)}},
		{name: "gte", filter: Gte("age", 20), want: []any{int64(20), int64(30)}},
		{name: "lt", filter: Lt("age", 20), want: []any{int64(10)}},
		{name: "lte", filter: Lte("age", 20), want: []any{int64(10), int64(20)}},
		{name: "gt against a string value", filter: Gt("age", "20"), want: nil},
		{name: "eq against a string value", filter: Eq("age", "20"), want: nil},
		{name: "gt on a field no document has", filter: Gt("ghost", 1), want: nil},
		{name: "eq on a field no document has", filter: Eq("ghost", nil), want: nil},
		{name: "gt on a field only some documents have", filter: Gt("age", 0), want: []any{int64(10), int64(20), int64(30)}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			docs := s.find(ctx, col, tt.filter)
			s.Equal(tt.want, s.values(docs, "age"))
		})
	}
}

func (s *FilterSuite) TestFind_StringComparisonIsLexicographic() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	for _, name := range []string{"ada", "brian", "carol"} {
		s.insert(ctx, col, map[string]any{"name": name})
	}

	tests := []struct {
		name   string
		filter Filter
		want   []any
	}{
		{name: "eq", filter: Eq("name", "brian"), want: []any{"brian"}},
		{name: "gt", filter: Gt("name", "b"), want: []any{"brian", "carol"}},
		{name: "lt", filter: Lt("name", "brian"), want: []any{"ada"}},
		{name: "lte", filter: Lte("name", "brian"), want: []any{"ada", "brian"}},
		{name: "gte", filter: Gte("name", "carol"), want: []any{"carol"}},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			docs := s.find(ctx, col, tt.filter)
			s.Equal(tt.want, s.values(docs, "name"))
		})
	}
}

func (s *FilterSuite) TestFind_EqOnNonOrderableValues() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	active := s.insert(ctx, col, map[string]any{"active": true})
	s.insert(ctx, col, map[string]any{"active": false})
	null := s.insert(ctx, col, map[string]any{"note": nil})

	docs := s.find(ctx, col, Eq("active", true))
	s.Require().Len(docs, 1)
	s.Equal(active, docs[0].ID)

	docs = s.find(ctx, col, Eq("note", nil))
	s.Require().Len(docs, 1)
	s.Equal(null, docs[0].ID)

	// A bool field is not orderable, so an ordering operator never matches it.
	s.Empty(s.find(ctx, col, Gt("active", true)))
}

func (s *FilterSuite) TestFind_TimeComparisonOrders() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var ids []AtlasID
	for i := range 3 {
		ids = append(ids, s.insert(ctx, col, map[string]any{"at": base.AddDate(0, 0, i)}))
	}

	docs := s.find(ctx, col, Eq("at", base.AddDate(0, 0, 1)))
	s.Require().Len(docs, 1)
	s.Equal(ids[1], docs[0].ID)

	docs = s.find(ctx, col, Gt("at", base.AddDate(0, 0, 1)))
	s.Require().Len(docs, 1)
	s.Equal(ids[2], docs[0].ID)

	docs = s.find(ctx, col, Lte("at", base.AddDate(0, 0, 1)))
	s.Len(docs, 2)
}

func (s *FilterSuite) TestFind_EmptyCollectionYieldsNoDocuments() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	s.Empty(s.find(ctx, col, Eq("anything", 1)))
}

// --- multi-page scans ---

func (s *FilterSuite) TestFind_SkipsPagesWithNoMatchesAndKeepsInsertionOrder() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	// The last documents inserted live on the last page, so every earlier
	// page contributes nothing and must be walked past.
	docs := s.find(ctx, col, Gte("n", 6))
	s.Equal([]any{int64(6), int64(7), int64(8)}, s.values(docs, "n"))

	docs = s.find(ctx, col, Lt("n", 2))
	s.Equal([]any{int64(0), int64(1)}, s.values(docs, "n"))

	docs = s.find(ctx, col, Gte("n", 0))
	s.Len(docs, 9)
	s.Equal(int64(0), docs[0].Fields["n"])
	s.Equal(int64(8), docs[8].Fields["n"])

	s.Empty(s.find(ctx, col, Eq("n", 100)))
}

func (s *FilterSuite) TestFindFunc_AppliesPredicateAcrossPages() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.FindFunc(ctx, func(doc Document) bool {
		n, ok := doc.Fields["n"].(int64)
		return ok && n%2 == 0
	})
	s.Require().NoError(err)

	docs := s.collect(cur)
	s.Equal([]any{int64(0), int64(2), int64(4), int64(6), int64(8)}, s.values(docs, "n"))
}

// --- limit / offset / Collect ---

func (s *FilterSuite) TestFind_WithLimit() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 0), WithLimit(3))
	s.Require().NoError(err)
	docs := s.collect(cur)
	s.Equal([]any{int64(0), int64(1), int64(2)}, s.values(docs, "n"))
}

func (s *FilterSuite) TestFind_WithOffset() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 0), WithOffset(6))
	s.Require().NoError(err)
	docs := s.collect(cur)
	s.Equal([]any{int64(6), int64(7), int64(8)}, s.values(docs, "n"))
}

func (s *FilterSuite) TestFind_WithOffsetAndLimit_Paginates() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 0), WithOffset(3), WithLimit(3))
	s.Require().NoError(err)
	docs := s.collect(cur)
	s.Equal([]any{int64(3), int64(4), int64(5)}, s.values(docs, "n"))
}

func (s *FilterSuite) TestFind_WithOffsetPastEveryMatch_YieldsNothing() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 0), WithOffset(100))
	s.Require().NoError(err)
	s.Empty(s.collect(cur))
}

func (s *FilterSuite) TestCursor_Collect_MatchesManualIterationAndClosesCursor() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 6))
	s.Require().NoError(err)

	docs, err := cur.Collect()
	s.Require().NoError(err)
	s.Equal([]any{int64(6), int64(7), int64(8)}, s.values(docs, "n"))

	// Collect leaves the cursor closed — further Next calls just report done.
	s.False(cur.Next())
	s.NoError(cur.Err())
}

func (s *FilterSuite) TestFind_ReflectsDeletesMadeBeforeTheScan() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	docs := s.find(ctx, col, Eq("n", 4))
	s.Require().Len(docs, 1)
	s.Require().NoError(col.Delete(ctx, docs[0].ID))

	s.Empty(s.find(ctx, col, Eq("n", 4)))
	s.Len(s.find(ctx, col, Gte("n", 0)), 8)
}

// --- cursor lifecycle ---

func (s *FilterSuite) TestFind_CollectionDroppedBeforeScan() {
	ctx := context.Background()
	db, col := s.newCollection(ctx)
	s.insert(ctx, col, map[string]any{"k": "v"})

	s.Require().NoError(db.DropCollection(ctx, "docs"))

	_, err := col.Find(ctx, Eq("k", "v"))
	s.ErrorIs(err, ErrCollectionNotFound)

	_, err = col.FindFunc(ctx, func(Document) bool { return true })
	s.ErrorIs(err, ErrCollectionNotFound)
}

func (s *FilterSuite) TestFind_AfterCloseReturnsErrClosed() {
	ctx := context.Background()
	db, col := s.newCollection(ctx)
	s.Require().NoError(db.Close())

	_, err := col.Find(ctx, Eq("k", "v"))
	s.ErrorIs(err, ErrClosed)

	_, err = col.FindFunc(ctx, func(Document) bool { return true })
	s.ErrorIs(err, ErrClosed)
}

func (s *FilterSuite) TestCursor_CloseStopsIterationAndIsIdempotent() {
	ctx := context.Background()
	_, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 0))
	s.Require().NoError(err)

	s.Require().True(cur.Next())
	s.Require().NoError(cur.Close())
	s.Require().NoError(cur.Close())

	s.False(cur.Next())
	s.NoError(cur.Err())

	_, err = cur.Document()
	s.Error(err)
}

func (s *FilterSuite) TestCursor_DatabaseClosedMidIteration() {
	ctx := context.Background()
	db, col := s.numberedDocs(ctx, 9)

	cur, err := col.Find(ctx, Gte("n", 0))
	s.Require().NoError(err)
	s.Require().True(cur.Next())

	s.Require().NoError(db.Close())

	s.False(cur.Next())
	s.ErrorIs(cur.Err(), ErrClosed)
}

func (s *FilterSuite) TestCursor_DocumentBeforeNext() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)
	s.insert(ctx, col, map[string]any{"k": "v"})

	cur, err := col.Find(ctx, Eq("k", "v"))
	s.Require().NoError(err)
	defer cur.Close()

	_, err = cur.Document()
	s.Error(err)

	s.Require().True(cur.Next())
	_, err = cur.Document()
	s.NoError(err)

	// Exhausted: the last document is no longer current either.
	s.False(cur.Next())
	_, err = cur.Document()
	s.Error(err)
}

func (s *FilterSuite) TestCursor_ErrSurfacesACorruptedRecordMidScan() {
	ctx := context.Background()
	db, col := s.numberedDocs(ctx, 6)

	_, slot, err := storage.FindCollectionSlot(ctx, db.f, db.h, "docs")
	s.Require().NoError(err)

	head, err := storage.ReadPage(ctx, db.f, db.h.PageSize, slot.Head)
	s.Require().NoError(err)
	second := binary.BigEndian.Uint32(head[1:5])
	s.Require().NotEqual(uint32(0), second)

	s.corruptFirstRecord(ctx, db, second)

	cur, err := col.Find(ctx, Gte("n", 0))
	s.Require().NoError(err)
	defer cur.Close()

	// Everything on the intact first page still reads back before the
	// corruption is reached.
	var seen int
	for cur.Next() {
		seen++
	}
	s.Greater(seen, 0)
	s.ErrorIs(cur.Err(), ErrCorruptedDocument)
}

// corruptFirstRecord flips a byte inside the payload of the first live
// record on pageNum, then rewrites the page with a recomputed page checksum
// so the damage surfaces as a per-record mismatch rather than a page-level
// one. It reproduces the on-disk data-page layout directly: a 13-byte header
// (type, next, free_start, slot_count, page_checksum) followed by a slot
// array of 9-byte entries (flags, offset, length, checksum) growing backward
// from the end of the page.
func (s *FilterSuite) corruptFirstRecord(ctx context.Context, db *DB, pageNum uint32) {
	const (
		slotSize      = 9
		tombstoneFlag = 1
	)

	pageSize := db.h.PageSize
	page, err := storage.ReadPage(ctx, db.f, pageSize, pageNum)
	s.Require().NoError(err)

	slotCount := uint32(binary.BigEndian.Uint16(page[7:9]))
	s.Require().Greater(slotCount, uint32(0))

	type recordSlot struct {
		flags          byte
		offset, length uint32
	}
	var slots []recordSlot
	for i := range slotCount {
		off := pageSize - (i+1)*slotSize
		slots = append(slots, recordSlot{
			flags:  page[off],
			offset: uint32(binary.BigEndian.Uint16(page[off+1 : off+3])),
			length: uint32(binary.BigEndian.Uint16(page[off+3 : off+5])),
		})
	}

	corrupted := false
	for _, slot := range slots {
		if slot.flags&tombstoneFlag != 0 {
			continue
		}
		page[slot.offset+AtlasIDSize] ^= 0xFF // payload bytes, past the id
		corrupted = true
		break
	}
	s.Require().True(corrupted)

	sum := crc32.NewIEEE()
	sum.Write(page[0:9])
	sum.Write(page[pageSize-slotCount*slotSize : pageSize])
	for _, slot := range slots {
		if slot.flags&tombstoneFlag != 0 {
			continue
		}
		sum.Write(page[slot.offset : slot.offset+slot.length])
	}
	binary.BigEndian.PutUint32(page[9:13], sum.Sum32())

	cycle, err := storage.NewJournalCycle(ctx, db.f, db.h)
	s.Require().NoError(err)
	s.Require().NoError(storage.WritePage(ctx, db.f, db.h, cycle, pageNum, page))
	s.Require().NoError(cycle.Commit(ctx, db.f, db.h))
}
