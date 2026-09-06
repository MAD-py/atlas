package atlas

import (
	"context"

	"github.com/MAD-py/atlas/internal/encoding"
	"github.com/MAD-py/atlas/internal/storage"
)

// Update is a changeset for Collection.Update: Set adds or overwrites the
// given fields, Unset removes the named ones, and every other field is left
// untouched. Using a changeset instead of a plain map keeps "remove this
// field" distinguishable from "set it to null" — and if the same key
// appears in both Set and Unset, Set wins.
type Update struct {
	Set   map[string]any
	Unset []string
}

// Collection is a lightweight handle, not a cache of its catalog slot:
// Head/Tail/DocCount go stale the moment any document is inserted or
// deleted, so a slot must be re-resolved at the time of each operation
// rather than stored here.
type Collection struct {
	db   *DB
	name string
}

// Insert stores doc and returns its new id. Insert always mints the stored
// id itself: a non-zero doc.ID is ignored rather than honored or rejected.
// It returns ErrDocumentTooLarge if the encoded document doesn't fit a
// single blank page.
func (c *Collection) Insert(ctx context.Context, doc Document) (AtlasID, error) {
	if c.db.closed {
		return AtlasID{}, ErrClosed
	}

	id, err := NewAtlasID()
	if err != nil {
		return AtlasID{}, wrapCollectionErr(err, c.name)
	}

	record, err := encoding.Marshal(id, doc.Fields)
	if err != nil {
		return AtlasID{}, wrapDocumentErr(err, c.name, id)
	}

	ref, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return AtlasID{}, wrapCollectionErr(err, c.name)
	}

	err = c.db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		_, _, err := storage.InsertRecord(ctx, c.db.f, c.db.h, cycle, ref, &slot, record)
		return err
	})
	if err != nil {
		return AtlasID{}, wrapDocumentErr(err, c.name, id)
	}
	return id, nil
}

// FindByID returns the document with the given id. It returns
// ErrDocumentNotFound if no such document exists in the collection.
func (c *Collection) FindByID(ctx context.Context, id AtlasID) (Document, error) {
	if c.db.closed {
		return Document{}, ErrClosed
	}

	_, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return Document{}, wrapCollectionErr(err, c.name)
	}

	record, _, _, err := storage.FindRecordByID(ctx, c.db.f, c.db.h, slot.Head, id)
	if err != nil {
		return Document{}, wrapDocumentErr(err, c.name, id)
	}

	storedID, fields, err := encoding.Unmarshal(record)
	if err != nil {
		return Document{}, wrapDocumentErr(err, c.name, id)
	}
	return Document{ID: storedID, Fields: fields}, nil
}

// Update merges changes into the existing document with the given id:
// fields named in changes.Set are added or overwritten, fields named in
// changes.Unset are removed, and every other field is left as it was. It
// returns ErrDocumentNotFound if no such document exists, or
// ErrDocumentTooLarge if the merged document no longer fits a blank page.
func (c *Collection) Update(ctx context.Context, id AtlasID, changes Update) error {
	if c.db.closed {
		return ErrClosed
	}

	return c.rewriteRecord(ctx, id, func(head uint32) ([]byte, error) {
		record, _, _, err := storage.FindRecordByID(ctx, c.db.f, c.db.h, head, id)
		if err != nil {
			return nil, err
		}
		_, fields, err := encoding.Unmarshal(record)
		if err != nil {
			return nil, err
		}

		// Unset is applied before Set, so a key given in both ends up set:
		// the explicit new value is the more specific of the two.
		for _, name := range changes.Unset {
			delete(fields, name)
		}
		for name, value := range changes.Set {
			fields[name] = value
		}
		return encoding.Marshal(id, fields)
	})
}

// Replace discards every field of the document with the given id and stores
// fields in their place, keeping the same id. Unlike Update, fields not
// present in the new map do not survive. It returns ErrDocumentNotFound if
// no such document exists, or ErrDocumentTooLarge if fields doesn't fit a
// blank page.
func (c *Collection) Replace(ctx context.Context, id AtlasID, fields map[string]any) error {
	if c.db.closed {
		return ErrClosed
	}

	return c.rewriteRecord(ctx, id, func(uint32) ([]byte, error) {
		return encoding.Marshal(id, fields)
	})
}

// rewriteRecord stores a new record for an already-existing id as a
// delete-then-reinsert of that same id inside one journal cycle, so a crash
// between them can't drop the document. Resolving the slot and computing the
// new record via build both only read/compute — neither writes anything —
// so both run before the cycle opens, the same way Insert resolves its own
// pre-write work first: a lookup or marshal failure then never pays for a
// journal file that was always going to be aborted. build is called with the
// collection's current head page — enough for it to read the stored record
// when the new content depends on it — and returns the record to store;
// whatever it returns keeps id, no new one is minted.
func (c *Collection) rewriteRecord(ctx context.Context, id AtlasID, build func(head uint32) ([]byte, error)) error {
	ref, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return wrapCollectionErr(err, c.name)
	}

	record, err := build(slot.Head)
	if err != nil {
		return wrapDocumentErr(err, c.name, id)
	}

	err = c.db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		// DeleteRecord can free the page the record lived on, mutating slot
		// in place, so the reinsert must use that same mutated slot.
		if err := storage.DeleteRecord(ctx, c.db.f, c.db.h, cycle, ref, &slot, id); err != nil {
			return err
		}
		_, _, err = storage.InsertRecord(ctx, c.db.f, c.db.h, cycle, ref, &slot, record)
		return err
	})
	if err != nil {
		return wrapDocumentErr(err, c.name, id)
	}
	return nil
}

// Delete removes the document with the given id from the collection. It
// returns ErrDocumentNotFound if no such document exists.
func (c *Collection) Delete(ctx context.Context, id AtlasID) error {
	if c.db.closed {
		return ErrClosed
	}

	ref, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return wrapCollectionErr(err, c.name)
	}

	err = c.db.withJournalCycle(ctx, func(cycle *storage.JournalCycle) error {
		return storage.DeleteRecord(ctx, c.db.f, c.db.h, cycle, ref, &slot, id)
	})
	if err != nil {
		return wrapDocumentErr(err, c.name, id)
	}
	return nil
}

// Count returns the number of documents in the collection. It reads the
// catalog slot's running counter instead of scanning the data-page chain —
// O(1), maintained incrementally by every Insert/Delete/Update/Replace.
func (c *Collection) Count(ctx context.Context) (int, error) {
	if c.db.closed {
		return 0, ErrClosed
	}

	_, slot, err := storage.FindCollectionSlot(ctx, c.db.f, c.db.h, c.name)
	if err != nil {
		return 0, wrapCollectionErr(err, c.name)
	}
	return int(slot.DocCount), nil
}
