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

// operator and Filter's fields are deliberately unexported: Eq/Gt/Lt/Gte/Lte
// are the only sanctioned way to build a Filter, so a caller can never
// construct one with a field left unset or an operator that doesn't match
// its own value's shape.
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

type Filter struct {
	field string
	op    operator
	value any
}

func Eq(field string, value any) Filter {
	return Filter{field: field, op: opEq, value: value}
}

func Gt(field string, value any) Filter {
	return Filter{field: field, op: opGt, value: value}
}

func Lt(field string, value any) Filter {
	return Filter{field: field, op: opLt, value: value}
}

func Gte(field string, value any) Filter {
	return Filter{field: field, op: opGte, value: value}
}

func Lte(field string, value any) Filter {
	return Filter{field: field, op: opLte, value: value}
}

// matches never errors and never panics: a scan sees every document in the
// collection, and a field being absent, or holding a type the filter can't
// be compared against, is ordinary data — those documents simply don't
// match.
func (f Filter) matches(doc Document) bool {
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

type FindOption func(*findConfig)

type findConfig struct {
	limit  int
	offset int
}

// n <= 0 means unlimited (the default). Once reached, Next stops before
// fetching another page.
func WithLimit(n int) FindOption {
	return func(c *findConfig) { c.limit = n }
}

// n <= 0 means no skipping (the default). A skipped match still has to be
// decoded and tested — there's no index to jump ahead with — but it's
// discarded immediately rather than held onto.
func WithOffset(n int) FindOption {
	return func(c *findConfig) { c.offset = n }
}

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

// Next serves documents out of the page batch already in hand, fetching the
// next page only once that batch is drained.
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

func (cur *Cursor) Document() (Document, error) {
	if cur.closed || !cur.valid {
		return Document{}, errNoCurrentDocument
	}
	return cur.current, nil
}

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

func (c *Collection) Find(ctx context.Context, filter Filter, opts ...FindOption) (*Cursor, error) {
	return c.FindFunc(ctx, filter.matches, opts...)
}

// FindFunc scans the whole collection by construction: the engine can't
// inspect a closure the way it can a Filter.
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
