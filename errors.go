package atlas

import (
	"errors"
	"fmt"
	"strings"

	"github.com/MAD-py/atlas/internal/file"
	"github.com/MAD-py/atlas/internal/storage"
)

// --- Codes ---

// Code is the stable, machine-readable classification of an Error.
type Code int32

// These numbers are part of the public contract: a caller — or a future
// non-Go binding — may persist and compare them as plain integers. A value
// assigned to a name here is never reassigned and never reused; a new code
// takes an unused number in its block (or an unused block entirely). That is
// why every one is an explicit literal: inserting a line into an iota block
// would silently renumber everything below it.
const (
	CodeUnknown Code = 0

	// 1xx — lifecycle
	CodeClosed Code = 100
	CodeLocked Code = 101

	// 2xx — collection
	CodeCollectionNotFound      Code = 200
	CodeCollectionAlreadyExists Code = 201
	CodeCollectionNameTooLong   Code = 202

	// 3xx — document
	CodeDocumentNotFound Code = 300
	CodeDocumentTooLarge Code = 301

	// 4xx — filter/query
	CodeInvalidFilter Code = 400

	// 5xx — file / format integrity
	CodeEmptyDatabasePath   Code = 500
	CodeInvalidAtlasID      Code = 501
	CodeNotAnAtlasFile      Code = 502
	CodeIncompatibleVersion Code = 503
	CodeCorruptedHeader     Code = 504
	CodeCorruptedPage       Code = 505
	CodeCorruptedDocument   Code = 506
	CodeJournalMissing      Code = 507

	// 6xx — cursor
	CodeNoCurrentDocument Code = 600

	// fallback — deliberately far past every category above, so a future
	// category never has to compete with it for a number
	CodeInternal Code = 900
)

// A map rather than a switch: adding a code later is one new entry in any
// position, with no arm ordering to get wrong.
var codeNames = map[Code]string{
	CodeUnknown:                 "unknown",
	CodeClosed:                  "closed",
	CodeLocked:                  "locked",
	CodeCollectionNotFound:      "collection_not_found",
	CodeCollectionAlreadyExists: "collection_already_exists",
	CodeCollectionNameTooLong:   "collection_name_too_long",
	CodeDocumentNotFound:        "document_not_found",
	CodeDocumentTooLarge:        "document_too_large",
	CodeInvalidFilter:           "invalid_filter",
	CodeEmptyDatabasePath:       "empty_database_path",
	CodeInvalidAtlasID:          "invalid_atlas_id",
	CodeNotAnAtlasFile:          "not_an_atlas_file",
	CodeIncompatibleVersion:     "incompatible_version",
	CodeCorruptedHeader:         "corrupted_header",
	CodeCorruptedPage:           "corrupted_page",
	CodeCorruptedDocument:       "corrupted_document",
	CodeJournalMissing:          "journal_missing",
	CodeNoCurrentDocument:       "no_current_document",
	CodeInternal:                "internal",
}

func (c Code) String() string {
	if name, ok := codeNames[c]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", int32(c))
}

// --- Error ---

// Error is the concrete type of every error the public API returns: a stable
// Code plus whatever context the failing call knew about. Reach it with
// errors.As, or match a kind of failure with errors.Is against one of the
// sentinels below.
//
// The fields are unexported on purpose, not by style habit: a caller holding
// one of the shared package-level sentinels could otherwise overwrite a field
// on it and corrupt every future comparison against that value. The getters
// are read-only, and there is no exported constructor — only this package
// produces an *Error, so a Code can never be invented from outside.
type Error struct {
	code    Code
	message string

	path       string
	collection string
	documentID AtlasID

	field string
	value any

	err error
}

func (e *Error) Code() Code          { return e.code }
func (e *Error) Message() string     { return e.message }
func (e *Error) Path() string        { return e.path }
func (e *Error) Collection() string  { return e.collection }
func (e *Error) DocumentID() AtlasID { return e.documentID }
func (e *Error) Field() string       { return e.field }
func (e *Error) Value() any          { return e.value }

// Unwrap reaches the internal cause an error was translated from, for
// debugging. It is deliberately absent from Error's own message: the public
// message and the internal one say the same thing in nearly the same words.
func (e *Error) Unwrap() error { return e.err }

// Error renders as "[Atlas <code>] <message>", followed by ": " and every
// populated context field in a fixed order. The Code mnemonic is left out —
// it would only repeat the message in a different casing.
func (e *Error) Error() string {
	var parts []string
	if e.path != "" {
		parts = append(parts, fmt.Sprintf("path=%q", e.path))
	}
	if e.collection != "" {
		parts = append(parts, fmt.Sprintf("collection=%q", e.collection))
	}
	if e.documentID != (AtlasID{}) {
		parts = append(parts, fmt.Sprintf("id=%q", e.documentID.String()))
	}
	if e.field != "" {
		parts = append(parts, fmt.Sprintf("field=%q", e.field))
	}
	if e.value != nil {
		parts = append(parts, fmt.Sprintf("value=%v", e.value))
	}

	msg := fmt.Sprintf("[Atlas %d] %s", e.code, e.message)
	if len(parts) == 0 {
		return msg
	}
	return msg + ": " + strings.Join(parts, " ")
}

// Is compares by Code alone, which is what keeps errors.Is against a bare
// sentinel working: every error handed to a caller is freshly built with its
// own context, never the sentinel value itself.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && e.code == t.code
}

// --- Exported sentinels ---

// Ordered by ascending identifier length. Each carries only its code and base
// message: they are errors.Is targets, and the source of the code/message
// pair for the real errors built at the call sites, never returned as-is
// with context that belongs to a specific failure.
var (
	ErrClosed = &Error{code: CodeClosed, message: "database is closed"}
	ErrLocked = &Error{code: CodeLocked, message: "database file is already open by another connection"}

	ErrInternal = &Error{code: CodeInternal, message: "internal error"}

	ErrCorruptedPage = &Error{code: CodeCorruptedPage, message: "page checksum mismatch"}
	ErrInvalidFilter = &Error{code: CodeInvalidFilter, message: "invalid filter"}

	ErrInvalidAtlasID = &Error{code: CodeInvalidAtlasID, message: "invalid AtlasID string"}
	ErrJournalMissing = &Error{code: CodeJournalMissing, message: "dirty shutdown detected but no valid journal to recover"}
	ErrNotAnAtlasFile = &Error{code: CodeNotAnAtlasFile, message: "not a valid atlas database file"}

	ErrCorruptedHeader = &Error{code: CodeCorruptedHeader, message: "file header checksum mismatch"}

	ErrDocumentNotFound = &Error{code: CodeDocumentNotFound, message: "document not found"}
	ErrDocumentTooLarge = &Error{code: CodeDocumentTooLarge, message: "document exceeds maximum size"}

	ErrCorruptedDocument = &Error{code: CodeCorruptedDocument, message: "document checksum mismatch"}
	ErrEmptyDatabasePath = &Error{code: CodeEmptyDatabasePath, message: "database path must not be empty"}
	ErrNoCurrentDocument = &Error{code: CodeNoCurrentDocument, message: "cursor has no current document"}

	ErrCollectionNotFound = &Error{code: CodeCollectionNotFound, message: "collection not found"}

	ErrIncompatibleVersion = &Error{code: CodeIncompatibleVersion, message: "incompatible file format version"}

	ErrCollectionNameTooLong = &Error{code: CodeCollectionNameTooLong, message: "collection name exceeds maximum length"}

	ErrCollectionAlreadyExists = &Error{code: CodeCollectionAlreadyExists, message: "collection already exists"}
)

// --- Translation at the public boundary ---

// publicSentinel maps an internal sentinel to the public one carrying the
// equivalent code and wording. A nil result means no dedicated code exists
// for this failure.
func publicSentinel(err error) *Error {
	switch {
	case errors.Is(err, file.ErrLocked):
		return ErrLocked
	case errors.Is(err, storage.ErrCorruptedPage):
		return ErrCorruptedPage
	case errors.Is(err, storage.ErrJournalMissing):
		return ErrJournalMissing
	case errors.Is(err, storage.ErrNotAnAtlasFile):
		return ErrNotAnAtlasFile
	case errors.Is(err, storage.ErrCorruptedHeader):
		return ErrCorruptedHeader
	case errors.Is(err, storage.ErrDocumentNotFound):
		return ErrDocumentNotFound
	case errors.Is(err, storage.ErrDocumentTooLarge):
		return ErrDocumentTooLarge
	case errors.Is(err, storage.ErrCorruptedDocument):
		return ErrCorruptedDocument
	case errors.Is(err, file.ErrEmptyDatabasePath):
		return ErrEmptyDatabasePath
	case errors.Is(err, storage.ErrCollectionNotFound):
		return ErrCollectionNotFound
	case errors.Is(err, storage.ErrIncompatibleVersion):
		return ErrIncompatibleVersion
	case errors.Is(err, storage.ErrCollectionNameTooLong):
		return ErrCollectionNameTooLong
	case errors.Is(err, storage.ErrCollectionAlreadyExists):
		return ErrCollectionAlreadyExists
	}
	return nil
}

// wrapInternalErr turns any error raised below the public API into an
// *Error: the matching code and message when one exists, CodeInternal
// otherwise, so nothing reaches a caller untyped. meta supplies the context
// the call site actually knows; use the wrapPathErr/wrapCollectionErr/
// wrapDocumentErr helpers rather than calling this directly.
func wrapInternalErr(err error, meta Error) error {
	if err == nil {
		return nil
	}

	meta.err = err
	if sentinel := publicSentinel(err); sentinel != nil {
		meta.code, meta.message = sentinel.code, sentinel.message
		return &meta
	}

	// An unmapped failure keeps its own wording verbatim: internal sentinels
	// are already plain text, and Error() supplies the namespacing.
	meta.code = CodeInternal
	meta.message = err.Error()
	return &meta
}

func wrapPathErr(err error, path string) error {
	return wrapInternalErr(err, Error{path: path})
}

func wrapCollectionErr(err error, collection string) error {
	return wrapInternalErr(err, Error{collection: collection})
}

func wrapDocumentErr(err error, collection string, id AtlasID) error {
	return wrapInternalErr(err, Error{collection: collection, documentID: id})
}
