// Package atlas is an embedded document database: JSON-like documents
// grouped into collections, stored in a single .db file with no server
// process. Open a file with Open, create collections with
// DB.CreateCollection, and use Insert/FindByID/Update/Replace/Delete/Count
// and Find/FindFunc on a Collection to work with documents. Every error is
// an *Error: match its kind with errors.Is against a sentinel
// (ErrDocumentNotFound, ErrCollectionNotFound, ...), or reach its Code and
// the context it carries with errors.As.
package atlas

import (
	"context"
	"errors"
	"os"

	"github.com/MAD-py/atlas/internal/file"
	"github.com/MAD-py/atlas/internal/storage"
)

type atlasConfig struct {
	pageSize uint32
}

// Option configures file-creation-time settings. Ignored if the file already
// exists, since those values are already fixed in its header.
type Option func(*atlasConfig)

// WithPageSize sets the .db file's page size in bytes at creation time
// (4096 if omitted). Has no effect when opening a file that already exists,
// since page size is fixed once and stored in the file's header.
func WithPageSize(pageSize uint32) Option {
	return func(c *atlasConfig) {
		c.pageSize = pageSize
	}
}

// DB is an open connection to a single .db file. Only one DB may have a
// given file open at a time: Open takes an exclusive, cross-platform lock
// for as long as the connection stays open. A *DB and the Collections it
// creates are not safe for concurrent use from multiple goroutines.
type DB struct {
	f      *os.File
	h      *storage.Header
	closed bool
}

// Open creates path if it doesn't exist yet, takes an exclusive lock on it,
// then either bootstraps a brand-new file or recovers/reopens an existing
// one. Options only apply to the brand-new-file path — an existing file's
// page size is already fixed in its header. path does not need a .db
// extension; Open appends one if it's missing.
func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	cfg := &atlasConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	f, err := file.OpenDB(path)
	if err != nil {
		return nil, wrapPathErr(err, path)
	}

	if err := file.Lock(f); err != nil {
		f.Close()
		return nil, wrapPathErr(err, f.Name())
	}

	info, err := f.Stat()
	if err != nil {
		file.Unlock(f)
		f.Close()
		return nil, wrapPathErr(err, f.Name())
	}

	var h *storage.Header
	if info.Size() == 0 {
		h, err = storage.Bootstrap(ctx, f, cfg.pageSize)
	} else {
		// cfg.pageSize deliberately not passed here: page size is immutable
		// after creation, and this file already has one on disk.
		h, err = storage.RecoverIfNeeded(ctx, f)
	}
	if err != nil {
		file.Unlock(f)
		f.Close()
		return nil, wrapPathErr(err, f.Name())
	}

	return &DB{f: f, h: h}, nil
}

// Close releases the exclusive lock on the underlying file and closes it.
// It is idempotent: a second and later call returns nil without
// re-attempting the unlock/close.
func (db *DB) Close() error {
	if db.closed {
		return nil
	}
	db.closed = true

	unlockErr := file.Unlock(db.f)
	closeErr := db.f.Close()
	if unlockErr != nil || closeErr != nil {
		return errors.Join(unlockErr, closeErr)
	}
	return nil
}

// withJournalCycle commits fn's cycle on success, aborts (and joins the
// abort error, if any) on failure — mirrors bbolt's db.Update(func(tx) error).
func (db *DB) withJournalCycle(ctx context.Context, fn func(*storage.JournalCycle) error) error {
	cycle, err := storage.NewJournalCycle(ctx, db.f, db.h)
	if err != nil {
		return err
	}
	if err := fn(cycle); err != nil {
		if abortErr := cycle.Abort(ctx, db.f, db.h); abortErr != nil {
			return errors.Join(err, abortErr)
		}
		return err
	}
	return cycle.Commit(ctx, db.f, db.h)
}

// CreateCollection creates a new, empty collection named name and returns a
// handle to it. It returns ErrCollectionAlreadyExists if a collection with
// that name already exists, and ErrCollectionNameTooLong if name is too long.
func (db *DB) CreateCollection(ctx context.Context, name string) (*Collection, error) {
	if db.closed {
		return nil, ErrClosed
	}
	err := db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		_, err := storage.CreateCollectionSlot(ctx, db.f, db.h, cycle, name, false)
		return err
	})
	if err != nil {
		return nil, wrapCollectionErr(err, name)
	}
	return &Collection{db: db, name: name}, nil
}

// Collection returns a handle to the existing collection named name. It
// returns ErrCollectionNotFound if no such collection exists — Collection
// never creates one implicitly.
func (db *DB) Collection(ctx context.Context, name string) (*Collection, error) {
	if db.closed {
		return nil, ErrClosed
	}
	if _, _, err := storage.FindCollectionSlot(ctx, db.f, db.h, name); err != nil {
		return nil, wrapCollectionErr(err, name)
	}
	return &Collection{db: db, name: name}, nil
}

// DropCollection permanently deletes the named collection and every
// document in it. It returns ErrCollectionNotFound if no such collection
// exists.
func (db *DB) DropCollection(ctx context.Context, name string) error {
	if db.closed {
		return ErrClosed
	}
	_, slot, err := storage.FindCollectionSlot(ctx, db.f, db.h, name)
	if err != nil {
		return wrapCollectionErr(err, name)
	}

	err = db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		if err := storage.FreeCollectionDataPages(ctx, db.f, db.h, cycle, slot.Head); err != nil {
			return err
		}
		return storage.RemoveCollectionSlot(ctx, db.f, db.h, cycle, name)
	})
	if err != nil {
		return wrapCollectionErr(err, name)
	}
	return nil
}
