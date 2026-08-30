package atlas

import (
	"context"

	"github.com/MAD-py/atlas/internal/encoding"
	"github.com/MAD-py/atlas/internal/storage"
)

// Collection is a lightweight handle, not a cache of its catalog slot:
// Head/Tail/DocCount go stale the moment any document is inserted or
// deleted, so a slot must be re-resolved at the time of each operation
// rather than stored here.
type Collection struct {
	db   *DB
	name string
}

// Insert always mints the stored id: a non-zero doc.ID is ignored rather
// than honored or rejected.
func (c *Collection) Insert(ctx context.Context, doc Document) (AtlasID, error) {
	if c.db.closed {
		return AtlasID{}, ErrClosed
	}

	id, err := NewAtlasID()
	if err != nil {
		return AtlasID{}, err
	}

	record, err := encoding.Marshal(id, doc.Fields)
	if err != nil {
		return AtlasID{}, err
	}

	err = c.db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		ref, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
		if err != nil {
			return err
		}
		_, _, err = storage.InsertRecord(ctx, c.db.f, c.db.h, cycle, ref, &slot, record)
		return err
	})
	if err != nil {
		return AtlasID{}, wrapInternalErr(err, c.name)
	}
	return id, nil
}

func (c *Collection) FindByID(ctx context.Context, id AtlasID) (Document, error) {
	if c.db.closed {
		return Document{}, ErrClosed
	}

	_, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return Document{}, wrapInternalErr(err, c.name)
	}

	record, _, _, err := storage.FindRecordByID(ctx, c.db.f, c.db.h, slot.Head, id)
	if err != nil {
		return Document{}, wrapInternalErr(err, c.name)
	}

	storedID, fields, err := encoding.Unmarshal(record)
	if err != nil {
		return Document{}, err
	}
	return Document{ID: storedID, Fields: fields}, nil
}

func (c *Collection) Delete(ctx context.Context, id AtlasID) error {
	if c.db.closed {
		return ErrClosed
	}

	err := c.db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		ref, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
		if err != nil {
			return err
		}
		return storage.DeleteRecord(ctx, c.db.f, c.db.h, cycle, ref, &slot, id)
	})
	if err != nil {
		return wrapInternalErr(err, c.name)
	}
	return nil
}

// Count reads the catalog slot's running counter instead of scanning the
// data-page chain; the counter is maintained by every insert and delete.
func (c *Collection) Count(ctx context.Context) (int, error) {
	if c.db.closed {
		return 0, ErrClosed
	}

	_, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return 0, wrapInternalErr(err, c.name)
	}
	return int(slot.DocCount), nil
}
