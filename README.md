# Atlas

Atlas is an embedded document database for Go. It stores JSON-like documents in collections inside a single `.db` file — no server process, no separate driver, no runtime dependency beyond `golang.org/x/sys` (used for cross-platform file locking). Analogous to SQLite, but for documents instead of rows.

## Features

- Single-file storage, opened directly by the Go program that uses it.
- Schema-less documents (`map[string]any`) addressed by an engine-generated, chronologically sortable id (`AtlasID`).
- Crash-safe writes: every insert, update, delete, and collection change goes through a rollback journal before it touches the `.db` file.
- Structured filtering (`Eq`, `Gt`, `Lt`, `Gte`, `Lte`) and a raw-predicate escape hatch (`FindFunc`), both returning a paged, memory-bounded cursor.
- Partial updates (`Update`, add/overwrite/remove fields) and whole-document replacement (`Replace`).
- Cross-platform (Linux, macOS, Windows) exclusive file locking — one connection per `.db` file at a time.

## Install

```
go get github.com/MAD-py/atlas
```

Requires Go 1.26 or later.

## Quick start

```go
db, err := atlas.Open(ctx, "mydata") // creates mydata.db if it doesn't exist
defer db.Close()

users, err := db.CreateCollection(ctx, "users")

id, err := users.Insert(ctx, atlas.Document{
	Fields: map[string]any{"name": "Ada", "age": int64(36)},
})

doc, err := users.FindByID(ctx, id)
fmt.Println(doc.Fields["name"]) // Ada
```

The sections below cover every piece of the API this touches, and everything else it offers, each with its own short example.

## Opening a database

```go
func Open(ctx context.Context, path string, opts ...Option) (*DB, error)
func (db *DB) Close() error
```

```go
db, err := atlas.Open(ctx, "mydata")
defer db.Close()
```

`Open` creates `path` if it doesn't exist yet (no separate "create database" step, the same as SQLite) and takes an exclusive lock on it for as long as the connection stays open — a second `Open` call on the same file, from this process or any other, fails with `ErrLocked` until the first one is closed. `path` doesn't need a `.db` extension; `Open` appends one if it's missing, so `Open(ctx, "mydata")` and `Open(ctx, "mydata.db")` both operate on `mydata.db`. Passing an empty `path` returns `ErrEmptyDatabasePath`.

`Close` releases the lock and closes the underlying file. It's safe to call more than once — later calls are no-ops that return `nil`. Every other method on a closed `*DB` (and on any `*Collection`/`*Cursor` obtained from it) returns `ErrClosed`.

`Open` accepts functional options that only take effect when creating a brand-new file — they're ignored when reopening one that already exists, since those settings are fixed in the file's header at creation time. `WithPageSize` sets the file's internal page size in bytes (4096 if omitted; rarely worth changing):

```go
db, err := atlas.Open(ctx, "mydata", atlas.WithPageSize(8192))
```

## Collections

A collection is a named group of documents, analogous to a SQL table or a Mongo collection. Collections are created explicitly, not implicitly on first write — a typo in a collection name surfaces as an error instead of silently creating an empty collection.

```go
func (db *DB) CreateCollection(ctx context.Context, name string) (*Collection, error)
func (db *DB) Collection(ctx context.Context, name string) (*Collection, error)
func (db *DB) DropCollection(ctx context.Context, name string) error
```

```go
users, err := db.CreateCollection(ctx, "users") // ErrCollectionAlreadyExists if it exists
users, err  = db.Collection(ctx, "users")        // ErrCollectionNotFound if it doesn't
err         = db.DropCollection(ctx, "users")    // deletes it and every document in it
```

A `*Collection` handle is cheap: it doesn't cache anything about the collection's contents, so it never goes stale, but it also means every operation on it looks the collection up by name again.

## Documents and ids

```go
type Document struct {
	ID     AtlasID
	Fields map[string]any
}
```

```go
doc := atlas.Document{Fields: map[string]any{"name": "Ada", "age": int64(36)}}
```

`Fields` can hold `nil`, `bool`, any signed Go integer type, `float64`, `string`, `time.Time`, `[]any`, or `map[string]any` for nested documents — a value of any other type makes a write fail. Values read back from the database always come back as the same handful of concrete types regardless of what you inserted: any integer type becomes `int64`, for instance.

`ID` is always an `AtlasID`, a 12-byte id the engine generates itself — setting `Document.ID` before an insert has no effect. `AtlasID`'s bytes are ordered so two ids compare in the order they were created, and that holds for the 24-character hex string form too:

```go
id, err := atlas.NewAtlasID()  // rarely needed directly — Insert already does this
s := id.String()               // 24-character lowercase hex
back, err := atlas.ParseAtlasID(s)
```

`AtlasID` also implements `encoding.TextMarshaler`/`TextUnmarshaler`, so `encoding/json` renders it as that same hex string automatically instead of an array of 12 numbers — a `Document` round-trips through `json.Marshal`/`Unmarshal` without any extra work.

## Inserting documents

```go
id, err := users.Insert(ctx, atlas.Document{
	Fields: map[string]any{"name": "Ada", "age": int64(36)},
})
```

`Insert` stores `doc.Fields` and returns the id it minted for the document. It returns `ErrDocumentTooLarge` if the encoded document doesn't fit within a single internal page — there's no overflow mechanism for oversized documents in the current version.

## Reading a document by id

```go
doc, err := users.FindByID(ctx, id) // ErrDocumentNotFound if id doesn't exist
```

## Updating documents

```go
type Update struct {
	Set   map[string]any
	Unset []string
}

func (c *Collection) Update(ctx context.Context, id AtlasID, changes Update) error
func (c *Collection) Replace(ctx context.Context, id AtlasID, fields map[string]any) error
```

`Update` merges a changeset into the document's existing fields: every key in `Set` is added or overwritten, every name in `Unset` is removed, and everything else is left exactly as it was. If the same key appears in both, `Set` wins:

```go
err := users.Update(ctx, id, atlas.Update{
	Set:   map[string]any{"age": int64(38), "city": "Buenos Aires"},
	Unset: []string{"nickname"},
})
```

`Replace` is the blunter tool: it discards every field the document had and stores the given map instead, keeping the same id:

```go
err := users.Replace(ctx, id, map[string]any{"name": "Ada Lovelace"}) // every other field is gone
```

Use `Replace` when you deliberately want a clean slate; use `Update` for the much more common "change a couple of fields" case — `Replace(ctx, id, map[string]any{"age": 31})` would silently delete every other field the document had. Both return `ErrDocumentNotFound` if `id` doesn't exist, and `ErrDocumentTooLarge` if the result no longer fits a page.

## Deleting documents

```go
err := users.Delete(ctx, id) // ErrDocumentNotFound if id doesn't exist
```

## Counting documents

```go
n, err := users.Count(ctx) // O(1) — a running counter, not a scan
```

## Querying

```go
type Filter struct{ /* ... */ } // build with Eq, Gt, Lt, Gte, Lte

func Eq(field string, value any) Filter
func Gt(field string, value any) Filter
func Lt(field string, value any) Filter
func Gte(field string, value any) Filter
func Lte(field string, value any) Filter

func (c *Collection) Find(ctx context.Context, filter Filter, opts ...FindOption) (*Cursor, error)
func (c *Collection) FindFunc(ctx context.Context, pred func(Document) bool, opts ...FindOption) (*Cursor, error)
```

`Find` scans the whole collection for documents matching a `Filter` — there's no secondary index yet, so every `Find` reads every document once:

```go
cur, err := users.Find(ctx, atlas.Gte("age", 18))
```

A `Filter` never errors and never panics, no matter what the collection actually contains: a document missing the field simply doesn't match, and a field holding a type the value can't meaningfully compare against (say, `Gt("age", 18)` against a document where `"age"` is a string) also just doesn't match — it's treated as ordinary, heterogeneous data, not a fault. Numbers compare by value regardless of their specific Go type on either side (an `int` filter value matches an `int64` field, for instance), strings compare lexicographically, and `time.Time` values compare chronologically. Two values that aren't both numbers, both strings, or both times are compared with plain equality for `Eq` and never match for the ordering operators.

`FindFunc` is the escape hatch for anything a `Filter` can't express — it calls an arbitrary predicate for every document instead:

```go
cur, err := users.FindFunc(ctx, func(doc atlas.Document) bool {
	name, _ := doc.Fields["name"].(string)
	return strings.HasPrefix(name, "A")
})
```

Both return a `*Cursor`, not a slice, so scanning a large collection never loads every match into memory at once:

```go
type Cursor struct{ /* ... */ }

func (cur *Cursor) Next() bool
func (cur *Cursor) Document() (Document, error)
func (cur *Cursor) Err() error
func (cur *Cursor) Close() error
func (cur *Cursor) Collect() ([]Document, error)
```

Call `Next` to advance, `Document` to read the current match, and `Close` when done:

```go
cur, err := users.Find(ctx, atlas.Eq("status", "active"))
defer cur.Close()

for cur.Next() {
	doc, err := cur.Document()
	fmt.Println(doc.Fields["name"])
}
if err := cur.Err(); err != nil {
	// something went wrong partway through the scan
}
```

Check `Err` after the loop ends — `Next` returning `false` means either the scan finished normally (`Err` is `nil`) or something stopped it early. If the result is small enough to want as a plain slice, call `Collect` instead of writing the loop:

```go
docs, err := cur.Collect() // also closes the cursor
```

`WithLimit`/`WithOffset` bound how much of the collection a scan touches, for simple pagination:

```go
cur, err := users.Find(ctx, atlas.Gte("age", 0), atlas.WithLimit(20), atlas.WithOffset(40)) // page 3, 20 per page
```

`WithLimit` stops the scan as soon as enough matches are found, without reading further pages. `WithOffset` still has to check every skipped match against the filter — there's no index to jump ahead with — but discards each one immediately instead of holding it, so it stays within the same bounded-memory guarantee as the rest of `Cursor`.

## Errors

Every error Atlas returns can be a sentinel value, checked with `errors.Is`:

```go
_, err := users.FindByID(ctx, id)
if errors.Is(err, atlas.ErrDocumentNotFound) {
	// handle a missing document
}
```

| Sentinel | Meaning |
|---|---|
| `ErrClosed` | The `*DB` (or a `Collection`/`Cursor` from it) was already closed. |
| `ErrLocked` | The `.db` file is already open by another connection. |
| `ErrEmptyDatabasePath` | `Open` was called with an empty path. |
| `ErrNotAnAtlasFile` | The file at the given path isn't a `.db` file this engine created. |
| `ErrIncompatibleVersion` | The file was created by an incompatible version of this format. |
| `ErrCorruptedHeader` | The file's header failed its checksum. |
| `ErrCorruptedPage` | A page failed its checksum. |
| `ErrCorruptedDocument` | A document failed its checksum. |
| `ErrJournalMissing` | The file was left in a dirty state by a crash, with no valid journal to recover it. |
| `ErrCollectionNotFound` | No collection with that name exists. |
| `ErrCollectionAlreadyExists` | `CreateCollection` was called with a name already in use. |
| `ErrCollectionNameTooLong` | A collection name exceeded the maximum length. |
| `ErrDocumentNotFound` | No document with that id exists in the collection. |
| `ErrDocumentTooLarge` | A document doesn't fit within a single internal page. |
| `ErrInvalidAtlasID` | `ParseAtlasID`/`UnmarshalText` was given a string that isn't a valid `AtlasID`. |

## Concurrency

One `*DB` corresponds to one open `.db` file, exclusively locked against other processes for as long as it's open. Within a process, `*DB` and its `Collection`/`Cursor` values are not safe for concurrent use from multiple goroutines without external synchronization.

## Internals

The public API in this package sits on top of three internal packages that own the on-disk format and are not importable outside this module:

| Package | Owns |
|---|---|
| [`internal/storage`](internal/storage/README.md) | The `.db` file's binary layout — pages, the collection catalog, the free list, and the rollback journal. |
| [`internal/encoding`](internal/encoding/README.md) | The TLV codec that turns a document's fields into the bytes `internal/storage` stores opaquely, and back. |
| [`internal/file`](internal/file/README.md) | Cross-platform file locking, and the naming/permission conventions for the `.db` file and its journal. |

See the linked READMEs for the exact byte-level format.
