package atlas

import (
	"cmp"
	"context"
	"math"
	"reflect"
	"time"

	"github.com/MAD-py/atlas/internal/encoding"
	"github.com/MAD-py/atlas/internal/storage"
)

// operator is a leafFilter's comparison kind.
type operator int

const (
	opEq operator = iota
	opGt
	opLt
	opGte
	opLte
)

func (op operator) satisfiedBy(order int) bool {
	switch op {
	case opEq:
		return order == 0
	case opGt:
		return order > 0
	case opLt:
		return order < 0
	case opGte:
		return order >= 0
	case opLte:
		return order <= 0
	default:
		return false
	}
}

// Filter is either a single comparison condition (built with Eq, Gt, Lt,
// Gte, or Lte) or a composite of other Filters (And, Or), nestable to any
// depth, used with Collection.Find. These functions are the only way to
// construct one. A leaf comparison whose field is missing from a document,
// or whose value the filter can't meaningfully compare against, simply
// doesn't match — Find never errors or panics because of a Filter.
type Filter interface {
	matches(doc Document) bool
}

// leafFilter is a single comparison: a field name, a comparison operator,
// and the value to compare against.
type leafFilter struct {
	field string
	op    operator
	value any
}

// Eq builds a Filter matching documents whose field equals value.
func Eq(field string, value any) Filter {
	return leafFilter{field: field, op: opEq, value: value}
}

// Gt builds a Filter matching documents whose field is greater than value.
func Gt(field string, value any) Filter {
	return leafFilter{field: field, op: opGt, value: value}
}

// Lt builds a Filter matching documents whose field is less than value.
func Lt(field string, value any) Filter {
	return leafFilter{field: field, op: opLt, value: value}
}

// Gte builds a Filter matching documents whose field is greater than or
// equal to value.
func Gte(field string, value any) Filter {
	return leafFilter{field: field, op: opGte, value: value}
}

// Lte builds a Filter matching documents whose field is less than or equal
// to value.
func Lte(field string, value any) Filter {
	return leafFilter{field: field, op: opLte, value: value}
}

// andFilter matches a document only if every one of filters matches it.
type andFilter struct {
	filters []Filter
}

// orFilter matches a document if any one of filters matches it.
type orFilter struct {
	filters []Filter
}

// And builds a Filter matching a document only if every one of filters
// matches it. And() with no filters matches every document (vacuously true).
func And(filters ...Filter) Filter {
	return andFilter{filters}
}

// Or builds a Filter matching a document if any one of filters matches it.
// Or() with no filters matches no document (vacuously false).
func Or(filters ...Filter) Filter {
	return orFilter{filters}
}

func (f andFilter) matches(doc Document) bool {
	for _, sub := range f.filters {
		if !sub.matches(doc) {
			return false
		}
	}
	return true
}

func (f orFilter) matches(doc Document) bool {
	for _, sub := range f.filters {
		if sub.matches(doc) {
			return true
		}
	}
	return false
}

// matches never errors and never panics: a scan sees every document in the
// collection, and a field being absent, or holding a type the filter can't
// be compared against, is ordinary data — those documents simply don't
// match.
func (f leafFilter) matches(doc Document) bool {
	stored, ok := doc.Fields[f.field]
	if !ok {
		return false
	}

	// A stored integer always decodes as int64 (internal/encoding.Marshal
	// accepts no other integer width), while a filter literal written in Go
	// source can be any integer type — so when both sides are integer-kind
	// they're compared as int64 first, exact, nothing to lose. float64 is
	// only used once a real float is involved, and only by promoting the
	// integer side up to float — never the reverse: truncating a float down
	// to compare as an integer would silently call e.g. 30.5 equal to a
	// stored 30, a wrong answer, not just an imprecise one.
	if a, aok := asInt64(stored); aok {
		if b, bok := asInt64(f.value); bok {
			return f.op.satisfiedBy(cmp.Compare(a, b))
		}
	}
	if a, aok := asFloat(stored); aok {
		if b, bok := asFloat(f.value); bok {
			return f.op.satisfiedBy(cmp.Compare(a, b))
		}
	}
	if a, aok := stored.(string); aok {
		if b, bok := f.value.(string); bok {
			return f.op.satisfiedBy(cmp.Compare(a, b))
		}
	}
	if a, aok := stored.(time.Time); aok {
		if b, bok := f.value.(time.Time); bok {
			return f.op.satisfiedBy(a.Compare(b))
		}
	}

	if f.op == opEq {
		return reflect.DeepEqual(stored, f.value)
	}
	return false
}

// asInt64 converts an integer-kind value to int64 exactly. Tried before
// asFloat so two integers of different concrete widths compare with nothing
// lost, instead of both being routed through float64 (lossy past 2^53) even
// though int64 can hold either exactly. Only a uint/uint64 past
// math.MaxInt64 fails here, falling through to asFloat instead.
func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

// asFloat is the fallback for when asInt64 couldn't compare both sides as
// exact integers — either because one side is a genuine float, or an
// integer wide enough to overflow int64 — so it still accepts every integer
// kind too, promoting it up to float64 rather than leaving it unhandled.
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// FindOption configures a Find or FindFunc scan. See WithLimit and
// WithOffset.
type FindOption func(*findConfig)

type findConfig struct {
	limit  int
	offset int
}

// WithLimit stops a scan after n matches. n <= 0 means unlimited (the
// default). Once reached, Next stops before fetching another page.
func WithLimit(n int) FindOption {
	return func(c *findConfig) { c.limit = n }
}

// WithOffset skips the first n matches of a scan before it starts yielding
// any. n <= 0 means no skipping (the default). A skipped match still has to
// be decoded and tested — there's no index to jump ahead with — but it's
// discarded immediately rather than held onto.
func WithOffset(n int) FindOption {
	return func(c *findConfig) { c.offset = n }
}

// Cursor iterates the documents matched by a Find or FindFunc scan, one page
// of the collection at a time. Call Next to advance, Document to read the
// current match, Err to check what (if anything) stopped the scan, and
// Close when done — or call Collect to drain every remaining match into a
// slice at once.
//
// Nothing protects a scan from concurrent mutation: a write to the same
// collection between two Next calls can make the rest of the scan skip
// documents or return one twice, and if the write frees a page that is then
// reused back into this same collection's chain, the scan reads
// structurally valid data belonging to different documents than the ones it
// would otherwise have reached.
type Cursor struct {
	col   *Collection
	ctx   context.Context
	match func(Document) bool

	limit    int
	skip     int
	returned int

	buf      []storage.RecordAt
	pos      int
	nextPage uint32

	current Document
	valid   bool
	err     error
	closed  bool
}

// Next advances the cursor to the next matching document, returning false
// once the scan is exhausted, the cursor is closed, or an error occurs (see
// Err). It serves documents out of the page batch already in hand, fetching
// the next page only once that batch is drained.
func (cur *Cursor) Next() bool {
	if cur.closed || cur.err != nil {
		return false
	}
	if cur.col.db.closed {
		cur.valid = false
		cur.err = ErrClosed
		return false
	}
	if cur.limit > 0 && cur.returned >= cur.limit {
		cur.valid = false
		return false
	}

	for {
		for cur.pos < len(cur.buf) {
			record := cur.buf[cur.pos]
			cur.pos++

			id, fields, err := encoding.Unmarshal(record.Data)
			if err != nil {
				cur.valid = false
				cur.err = err
				return false
			}

			doc := Document{ID: id, Fields: fields}
			if cur.match != nil && !cur.match(doc) {
				continue
			}
			if cur.skip > 0 {
				cur.skip--
				continue
			}
			cur.current = doc
			cur.valid = true
			cur.returned++
			return true
		}

		cur.valid = false
		if cur.nextPage == 0 {
			return false
		}

		records, next, err := storage.NextPage(cur.ctx, cur.col.db.f, cur.col.db.h, cur.nextPage)
		if err != nil {
			cur.err = wrapInternalErr(err, cur.col.name)
			return false
		}
		if len(records) == 0 {
			cur.nextPage = 0
			return false
		}
		cur.buf, cur.pos, cur.nextPage = records, 0, next
	}
}

// Document returns the document the most recent call to Next matched. It
// returns an error if Next has not yet been called, or last returned false.
func (cur *Cursor) Document() (Document, error) {
	if cur.closed || !cur.valid {
		return Document{}, errNoCurrentDocument
	}
	return cur.current, nil
}

// Err returns the error that stopped the scan, if any. It is nil if the
// scan simply ran out of matching documents.
func (cur *Cursor) Err() error {
	return cur.err
}

// Close only marks the cursor done — a cursor holds no journal cycle and no
// file handle of its own, so there is nothing to release.
func (cur *Cursor) Close() error {
	cur.closed = true
	return nil
}

// Collect drains the cursor into a slice, holding every remaining match in
// memory at once — the cost Find/FindFunc avoid by default — and closes the
// cursor itself when done, successful or not.
func (cur *Cursor) Collect() ([]Document, error) {
	defer cur.Close()
	var docs []Document
	for cur.Next() {
		doc, err := cur.Document()
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return docs, nil
}

// Find scans the collection for documents matching filter and returns a
// cursor over them. Options bound how much of the collection the scan
// touches — see WithLimit and WithOffset.
func (c *Collection) Find(ctx context.Context, filter Filter, opts ...FindOption) (*Cursor, error) {
	return c.FindFunc(ctx, filter.matches, opts...)
}

// FindFunc scans the collection, calling pred for every document and
// returning a cursor over the ones it accepts. It scans the whole collection
// by construction: the engine can't inspect a closure the way it can a
// Filter.
func (c *Collection) FindFunc(ctx context.Context, pred func(Document) bool, opts ...FindOption) (*Cursor, error) {
	if c.db.closed {
		return nil, ErrClosed
	}

	var cfg findConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	_, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return nil, wrapInternalErr(err, c.name)
	}
	return &Cursor{
		col:      c,
		ctx:      ctx,
		match:    pred,
		nextPage: slot.Head,
		limit:    cfg.limit,
		skip:     cfg.offset,
	}, nil
}
