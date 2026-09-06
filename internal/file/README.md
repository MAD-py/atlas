# internal/file — OS-level file concerns

This package covers everything about the `.db` file and its journal that sits outside the binary format itself: acquiring an exclusive lock on the file, deriving the journal's path from the `.db` path, and opening both files with the right flags and permissions. `internal/storage` treats these as already-open `*os.File` values; it never locks, names, or opens a file itself.

## 1. Locking

```go
func Lock(f *os.File) error
func Unlock(f *os.File) error
```

Both functions have two platform-specific implementations behind the same signatures:

| Platform | Mechanism | Library |
|---|---|---|
| Unix (Linux, macOS, BSD) | `flock(2)`, non-blocking exclusive | `golang.org/x/sys/unix` |
| Windows | `LockFileEx` over the file's full byte range, non-blocking exclusive | `golang.org/x/sys/windows` |

`Lock` returns immediately — it never waits for the lock to become available. If another process already holds it, `Lock` returns `ErrLocked` (the process-specific error underneath is discarded on Unix's `EWOULDBLOCK` / Windows' `ERROR_LOCK_VIOLATION`; any other underlying error is wrapped around the same sentinel instead). `Unlock` releases a lock held by `f`.

## 2. Path derivation

```go
func DBPathFor(path string) (string, error)
func JournalPathFor(dbPath string) string
```

`DBPathFor` validates `path` and returns the actual `.db` path to open: it returns `ErrEmptyDatabasePath` for an empty string, and appends `.db` to `path` if it doesn't already end with that extension. It does not validate anything beyond emptiness — a path naming a directory, or with no filename component, is left for the OS's own `open()` call to reject.

`JournalPathFor` derives a journal's path from its `.db` file's own path: the same path with `.journal` appended, a sibling file in the same directory.

## 3. Opening files

```go
func OpenDB(path string) (*os.File, error)
func CreateJournal(dbFile *os.File) (*os.File, error)
func OpenJournalReadOnly(dbFile *os.File) (*os.File, error)
```

| Function | Resolves path via | Flags | Mode |
|---|---|---|---|
| `OpenDB` | `DBPathFor(path)` | `O_RDWR\|O_CREATE` | `0o644` |
| `CreateJournal` | `JournalPathFor(dbFile.Name())` | `O_RDWR\|O_CREATE\|O_TRUNC` | `0o600` |
| `OpenJournalReadOnly` | `JournalPathFor(dbFile.Name())` | `O_RDONLY` | — |

`OpenDB` is the only entry point that creates or opens the `.db` file itself; it validates and normalizes `path` through `DBPathFor` first. `CreateJournal` truncates any stale leftover journal from a previous cycle before returning a fresh, empty one. `OpenJournalReadOnly` is used during recovery to check whether a journal exists (and read it) without creating one — opening a missing journal with it returns the usual `os.IsNotExist` error, not an `internal/file` sentinel.

The `.db` file is more permissive (`0o644`, owner read/write and everyone else read) than the journal (`0o600`, owner only): a journal record holds a page's pre-modification bytes, which can describe data already superseded in the live `.db` by the time anyone reads it.

## 4. Errors

| Sentinel | Message |
|---|---|
| `ErrLocked` | `"database file is already open by another connection"` |
| `ErrEmptyDatabasePath` | `"database path must not be empty"` |
