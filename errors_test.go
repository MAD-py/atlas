package atlas

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/MAD-py/atlas/internal/storage"
)

type ErrorsSuite struct {
	suite.Suite
}

func TestErrors(t *testing.T) {
	suite.Run(t, new(ErrorsSuite))
}

func (s *ErrorsSuite) path() string {
	return filepath.Join(s.T().TempDir(), "test.db")
}

func (s *ErrorsSuite) newCollection(ctx context.Context, opts ...Option) (*DB, *Collection) {
	db, err := Open(ctx, s.path(), opts...)
	s.Require().NoError(err)
	s.T().Cleanup(func() { db.Close() })

	col, err := db.CreateCollection(ctx, "users")
	s.Require().NoError(err)
	return db, col
}

// --- Code ---

// Every code in the const block must have a name: a new one added without a
// codeNames entry shows up here as "unknown(...)" instead of its mnemonic.
func (s *ErrorsSuite) TestCodeString_NamesEveryDefinedCode() {
	tests := []struct {
		code Code
		want string
	}{
		{code: CodeUnknown, want: "unknown"},
		{code: CodeClosed, want: "closed"},
		{code: CodeLocked, want: "locked"},
		{code: CodeCollectionNotFound, want: "collection_not_found"},
		{code: CodeCollectionAlreadyExists, want: "collection_already_exists"},
		{code: CodeCollectionNameTooLong, want: "collection_name_too_long"},
		{code: CodeDocumentNotFound, want: "document_not_found"},
		{code: CodeDocumentTooLarge, want: "document_too_large"},
		{code: CodeInvalidFilter, want: "invalid_filter"},
		{code: CodeEmptyDatabasePath, want: "empty_database_path"},
		{code: CodeInvalidAtlasID, want: "invalid_atlas_id"},
		{code: CodeNotAnAtlasFile, want: "not_an_atlas_file"},
		{code: CodeIncompatibleVersion, want: "incompatible_version"},
		{code: CodeCorruptedHeader, want: "corrupted_header"},
		{code: CodeCorruptedPage, want: "corrupted_page"},
		{code: CodeCorruptedDocument, want: "corrupted_document"},
		{code: CodeJournalMissing, want: "journal_missing"},
		{code: CodeNoCurrentDocument, want: "no_current_document"},
		{code: CodeInternal, want: "internal"},
	}

	s.Require().Len(codeNames, len(tests))
	for _, tt := range tests {
		s.Run(tt.want, func() {
			s.Equal(tt.want, tt.code.String())
			s.NotContains(tt.code.String(), "unknown(")
		})
	}
}

func (s *ErrorsSuite) TestCodeString_UndefinedCodeRendersItsNumber() {
	s.Equal("unknown(742)", Code(742).String())
}

// --- Error ---

func (s *ErrorsSuite) TestError_Format() {
	id, err := ParseAtlasID("6710f2a3b4c5d6e7f8091023")
	s.Require().NoError(err)

	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{
			name: "no metadata",
			err:  &Error{code: CodeClosed, message: "database is closed"},
			want: "[Atlas 100] database is closed",
		},
		{
			name: "collection only",
			err:  &Error{code: CodeCollectionNotFound, message: "collection not found", collection: "users"},
			want: `[Atlas 200] collection not found: collection="users"`,
		},
		{
			name: "collection and id",
			err:  &Error{code: CodeDocumentNotFound, message: "document not found", collection: "users", documentID: id},
			want: `[Atlas 300] document not found: collection="users" id="6710f2a3b4c5d6e7f8091023"`,
		},
		{
			name: "field and value",
			err:  &Error{code: CodeInvalidFilter, message: "bad filter", field: "tags", value: []any{1, 2}},
			want: "[Atlas 400] bad filter: field=\"tags\" value=[1 2]",
		},
		{
			name: "every field, fixed order",
			err: &Error{
				code:       CodeCorruptedDocument,
				message:    "document checksum mismatch",
				path:       "/tmp/x.db",
				collection: "users",
				documentID: id,
				field:      "age",
				value:      31,
			},
			want: `[Atlas 506] document checksum mismatch: path="/tmp/x.db" collection="users" id="6710f2a3b4c5d6e7f8091023" field="age" value=31`,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			s.Equal(tt.want, tt.err.Error())
		})
	}
}

// Every sentinel is only ever an errors.Is target: a real error is built
// fresh with its own context and must still match on code alone.
func (s *ErrorsSuite) TestErrorIs_MatchesByCodeNotIdentity() {
	tests := []struct {
		sentinel *Error
		code     Code
	}{
		{sentinel: ErrClosed, code: CodeClosed},
		{sentinel: ErrLocked, code: CodeLocked},
		{sentinel: ErrInternal, code: CodeInternal},
		{sentinel: ErrCorruptedPage, code: CodeCorruptedPage},
		{sentinel: ErrInvalidFilter, code: CodeInvalidFilter},
		{sentinel: ErrInvalidAtlasID, code: CodeInvalidAtlasID},
		{sentinel: ErrJournalMissing, code: CodeJournalMissing},
		{sentinel: ErrNotAnAtlasFile, code: CodeNotAnAtlasFile},
		{sentinel: ErrCorruptedHeader, code: CodeCorruptedHeader},
		{sentinel: ErrDocumentNotFound, code: CodeDocumentNotFound},
		{sentinel: ErrDocumentTooLarge, code: CodeDocumentTooLarge},
		{sentinel: ErrCorruptedDocument, code: CodeCorruptedDocument},
		{sentinel: ErrEmptyDatabasePath, code: CodeEmptyDatabasePath},
		{sentinel: ErrCollectionNotFound, code: CodeCollectionNotFound},
		{sentinel: ErrIncompatibleVersion, code: CodeIncompatibleVersion},
		{sentinel: ErrCollectionNameTooLong, code: CodeCollectionNameTooLong},
		{sentinel: ErrCollectionAlreadyExists, code: CodeCollectionAlreadyExists},
		{sentinel: ErrNoCurrentDocument, code: CodeNoCurrentDocument},
	}

	for _, tt := range tests {
		s.Run(tt.code.String(), func() {
			s.Equal(tt.code, tt.sentinel.Code())

			fresh := &Error{code: tt.code, message: "rebuilt elsewhere", collection: "users"}
			s.ErrorIs(fresh, tt.sentinel)
			s.NotErrorIs(fresh, &Error{code: CodeUnknown})
		})
	}
}

// --- Structured context at real call sites ---

func (s *ErrorsSuite) TestFindByID_ErrorCarriesCollectionAndDocumentID() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	missing, err := NewAtlasID()
	s.Require().NoError(err)

	_, err = col.FindByID(ctx, missing)
	s.Require().ErrorIs(err, ErrDocumentNotFound)

	var atlasErr *Error
	s.Require().ErrorAs(err, &atlasErr)
	s.Equal(CodeDocumentNotFound, atlasErr.Code())
	s.Equal("document not found", atlasErr.Message())
	s.Equal("users", atlasErr.Collection())
	s.Equal(missing, atlasErr.DocumentID())
	s.Empty(atlasErr.Path())
	s.Empty(atlasErr.Field())
	s.Nil(atlasErr.Value())
	s.ErrorIs(atlasErr.Unwrap(), storage.ErrDocumentNotFound)
}

func (s *ErrorsSuite) TestOpen_LockedErrorCarriesPath() {
	ctx := context.Background()
	path := s.path()

	db, err := Open(ctx, path)
	s.Require().NoError(err)
	defer db.Close()

	_, err = Open(ctx, path)
	s.Require().ErrorIs(err, ErrLocked)

	var atlasErr *Error
	s.Require().ErrorAs(err, &atlasErr)
	s.Equal(CodeLocked, atlasErr.Code())
	s.Equal(path, atlasErr.Path())
	s.Empty(atlasErr.Collection())
}

func (s *ErrorsSuite) TestCreateCollection_ErrorCarriesCollection() {
	ctx := context.Background()
	db, _ := s.newCollection(ctx)

	_, err := db.CreateCollection(ctx, "users")
	s.Require().ErrorIs(err, ErrCollectionAlreadyExists)

	var atlasErr *Error
	s.Require().ErrorAs(err, &atlasErr)
	s.Equal(CodeCollectionAlreadyExists, atlasErr.Code())
	s.Equal("users", atlasErr.Collection())
	s.Equal(`[Atlas 201] collection already exists: collection="users"`, atlasErr.Error())
}

// An internal sentinel with no dedicated public code arrives as CodeInternal,
// with the original error still reachable via Unwrap.
func (s *ErrorsSuite) TestOpen_UnmappedInternalErrorBecomesCodeInternal() {
	ctx := context.Background()
	path := s.path()

	_, err := Open(ctx, path, WithPageSize(1))
	s.Require().Error(err)
	s.ErrorIs(err, ErrInternal)

	var atlasErr *Error
	s.Require().ErrorAs(err, &atlasErr)
	s.Equal(CodeInternal, atlasErr.Code())
	s.Equal(path, atlasErr.Path())
	s.Equal("invalid page size", atlasErr.Message())
	s.ErrorIs(atlasErr.Unwrap(), storage.ErrInvalidPageSize)
	s.Equal(`[Atlas 900] invalid page size: path="`+path+`"`, atlasErr.Error())
}

func (s *ErrorsSuite) TestParseAtlasID_ErrorCarriesInvalidAtlasIDCode() {
	tests := []struct {
		name string
		in   string
	}{
		{name: "not hex", in: "zzzzzzzzzzzzzzzzzzzzzzzz"},
		{name: "wrong length", in: "6710f2a3"},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			_, err := ParseAtlasID(tt.in)
			s.Require().ErrorIs(err, ErrInvalidAtlasID)

			var atlasErr *Error
			s.Require().ErrorAs(err, &atlasErr)
			s.Equal(CodeInvalidAtlasID, atlasErr.Code())
			s.Contains(atlasErr.Message(), "invalid AtlasID string: ")
		})
	}
}

// Calling Document without checking Next first is caller misuse, not a
// runtime outcome tied to any collection/document — but it's still an
// *Error like everything else the public API returns, not a bare error.
func (s *ErrorsSuite) TestCursorDocument_WithoutNextCarriesNoCurrentDocumentCode() {
	ctx := context.Background()
	_, col := s.newCollection(ctx)

	cur, err := col.Find(ctx, Eq("name", "ada"))
	s.Require().NoError(err)
	defer cur.Close()

	_, err = cur.Document()
	s.Require().ErrorIs(err, ErrNoCurrentDocument)

	var atlasErr *Error
	s.Require().ErrorAs(err, &atlasErr)
	s.Equal(CodeNoCurrentDocument, atlasErr.Code())
	s.Empty(atlasErr.Collection())
	s.Equal("[Atlas 600] cursor has no current document", atlasErr.Error())
}
