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

func WithPageSize(pageSize uint32) Option {
	return func(c *atlasConfig) {
		c.pageSize = pageSize
	}
}

type DB struct {
	f      *os.File
	h      *storage.Header
	closed bool
}

// Open creates path if it doesn't exist yet, takes an exclusive lock on it,
// then either bootstraps a brand-new file or recovers/reopens an existing
// one. Options only apply to the brand-new-file path — an existing file's
// page size is already fixed in its header.
func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	cfg := &atlasConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	f, err := file.OpenDB(path)
	if err != nil {
		return nil, wrapInternalErr(err, path)
	}

	if err := file.Lock(f); err != nil {
		f.Close()
		return nil, wrapInternalErr(err, f.Name())
	}

	info, err := f.Stat()
	if err != nil {
		file.Unlock(f)
		f.Close()
		return nil, err
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
		return nil, wrapInternalErr(err, f.Name())
	}

	return &DB{f: f, h: h}, nil
}

// Close is idempotent: a second and later call returns nil without
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

func (db *DB) CreateCollection(ctx context.Context, name string) (*Collection, error) {
	if db.closed {
		return nil, ErrClosed
	}
	err := db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		_, err := storage.CreateCollectionSlot(ctx, db.f, db.h, cycle, name, false)
		return err
	})
	if err != nil {
		return nil, wrapInternalErr(err, name)
	}
	return &Collection{db: db, name: name}, nil
}

func (db *DB) Collection(ctx context.Context, name string) (*Collection, error) {
	if db.closed {
		return nil, ErrClosed
	}
	if _, _, err := storage.FindCollectionSlot(ctx, db.f, db.h, name); err != nil {
		return nil, wrapInternalErr(err, name)
	}
	return &Collection{db: db, name: name}, nil
}

func (db *DB) DropCollection(ctx context.Context, name string) error {
	if db.closed {
		return ErrClosed
	}
	_, slot, err := storage.FindCollectionSlot(ctx, db.f, db.h, name)
	if err != nil {
		return wrapInternalErr(err, name)
	}

	err = db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		if err := storage.FreeCollectionDataPages(ctx, db.f, db.h, cycle, slot.Head); err != nil {
			return err
		}
		return storage.RemoveCollectionSlot(ctx, db.f, db.h, cycle, name)
	})
	if err != nil {
		return wrapInternalErr(err, name)
	}
	return nil
}
