package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mcp-knowledge-graph/internal/knowledge"
)

// fakeService is a minimal Service for protocol tests. Methods without a
// configured function fail with an internal error.
type fakeService struct {
	search      func(knowledge.SearchParams) (knowledge.SearchResponse, error)
	getSummary  func(uuid.UUID) (knowledge.DocumentSummary, error)
	editDoc     func(knowledge.EditDocumentInput) (knowledge.EditDocumentResult, error)
	getFulltext func(uuid.UUID, knowledge.BlockPageRequest) (knowledge.FulltextPage, error)
	calls       []string
}

var errUnexpected = errors.New("unexpected call: dial tcp 10.0.0.1:5432 password=secret")

func (f *fakeService) Search(_ context.Context, p knowledge.SearchParams) (knowledge.SearchResponse, error) {
	f.calls = append(f.calls, "Search")
	if f.search == nil {
		return knowledge.SearchResponse{}, errUnexpected
	}
	return f.search(p)
}

func (f *fakeService) GetSummary(_ context.Context, id uuid.UUID) (knowledge.DocumentSummary, error) {
	f.calls = append(f.calls, "GetSummary")
	if f.getSummary == nil {
		return knowledge.DocumentSummary{}, errUnexpected
	}
	return f.getSummary(id)
}

func (f *fakeService) GetParentList(context.Context, uuid.UUID) (knowledge.ParentList, error) {
	return knowledge.ParentList{}, errUnexpected
}

func (f *fakeService) GetChildrenPage(context.Context, uuid.UUID, knowledge.ChildrenPageRequest) (knowledge.ChildrenPage, error) {
	return knowledge.ChildrenPage{}, errUnexpected
}

func (f *fakeService) GetFulltextPage(_ context.Context, id uuid.UUID, req knowledge.BlockPageRequest) (knowledge.FulltextPage, error) {
	f.calls = append(f.calls, "GetFulltextPage")
	if f.getFulltext == nil {
		return knowledge.FulltextPage{}, errUnexpected
	}
	return f.getFulltext(id, req)
}

func (f *fakeService) GetPaths(context.Context, uuid.UUID, bool) (knowledge.PathSet, error) {
	return knowledge.PathSet{}, errUnexpected
}

func (f *fakeService) GetSourcesPage(context.Context, uuid.UUID, knowledge.SourcesPageRequest) (knowledge.SourcesPage, error) {
	return knowledge.SourcesPage{}, errUnexpected
}

func (f *fakeService) CreateDocument(context.Context, knowledge.CreateDocumentInput) (knowledge.CreateDocumentResult, error) {
	f.calls = append(f.calls, "CreateDocument")
	return knowledge.CreateDocumentResult{}, errUnexpected
}

func (f *fakeService) UpdateDocumentMetadata(context.Context, uuid.UUID, int64, knowledge.MetadataUpdate) (knowledge.DocumentSummary, error) {
	f.calls = append(f.calls, "UpdateDocumentMetadata")
	return knowledge.DocumentSummary{}, errUnexpected
}

func (f *fakeService) EditDocument(_ context.Context, in knowledge.EditDocumentInput) (knowledge.EditDocumentResult, error) {
	f.calls = append(f.calls, "EditDocument")
	if f.editDoc == nil {
		return knowledge.EditDocumentResult{}, errUnexpected
	}
	return f.editDoc(in)
}

func (f *fakeService) AddParentChecked(context.Context, knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	return knowledge.ParentChangeResult{}, errUnexpected
}

func (f *fakeService) RemoveParentChecked(context.Context, knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	return knowledge.ParentChangeResult{}, errUnexpected
}

func (f *fakeService) SetPrimaryParentChecked(context.Context, knowledge.ParentChange) (knowledge.ParentChangeResult, error) {
	return knowledge.ParentChangeResult{}, errUnexpected
}

func (f *fakeService) AddSourceChecked(context.Context, uuid.UUID, int64, knowledge.SourceInput) (knowledge.AddSourceResult, error) {
	f.calls = append(f.calls, "AddSourceChecked")
	return knowledge.AddSourceResult{}, errUnexpected
}

func newFakeSession(t *testing.T, svc *fakeService) *mcp.ClientSession {
	t.Helper()
	return connect(t, NewServer(svc, Options{Version: "test"}))
}

const docID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

func TestListToolsExposesTheContract(t *testing.T) {
	cs := newFakeSession(t, &fakeService{})
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	require.Len(t, res.Tools, 15)
	require.ElementsMatch(t, ToolNames, mapKeys(byName))

	readTools := []string{ToolSearch, ToolGetRoot, ToolGetSummary, ToolGetParents, ToolGetChildren, ToolGetFulltext, ToolGetPaths, ToolGetSources}
	for _, name := range ToolNames {
		tool := byName[name]
		assert.GreaterOrEqual(t, len(tool.Description), 150, "%s needs a useful description", name)
		assert.NotEmpty(t, tool.Title, name)
		require.NotNil(t, tool.Annotations, name)
		require.NotNil(t, tool.InputSchema, name)
		require.NotNil(t, tool.OutputSchema, name)

		schema := asMap(t, tool.InputSchema)
		assert.Equal(t, "object", schema["type"], name)
		assert.Equal(t, false, schema["additionalProperties"], "%s must reject unknown arguments", name)

		isRead := contains(readTools, name)
		assert.Equal(t, isRead, tool.Annotations.ReadOnlyHint, name)
		require.NotNil(t, tool.Annotations.OpenWorldHint, name)
		assert.False(t, *tool.Annotations.OpenWorldHint, name)
		if !isRead {
			require.NotNil(t, tool.Annotations.DestructiveHint, name)
		}
	}
	assert.False(t, byName[ToolCreateDocument].Annotations.IdempotentHint, "create_document is not idempotent")
	assert.True(t, *byName[ToolEditDocument].Annotations.DestructiveHint)
	assert.False(t, *byName[ToolAddSource].Annotations.DestructiveHint)

	// Required arguments are declared in the schema.
	assert.ElementsMatch(t, []any{"query"}, asMap(t, byName[ToolSearch].InputSchema)["required"])
	assert.ElementsMatch(t, []any{"title", "summary", "parent_ids"}, asMap(t, byName[ToolCreateDocument].InputSchema)["required"])
	assert.ElementsMatch(t, []any{"document_id", "expected_version", "operations"}, asMap(t, byName[ToolEditDocument].InputSchema)["required"])
	assert.ElementsMatch(t, []any{"document_id", "parent_id", "expected_version"}, asMap(t, byName[ToolAddParent].InputSchema)["required"])
	assert.ElementsMatch(t, []any{"document_id", "expected_version", "kind", "source_uri"}, asMap(t, byName[ToolAddSource].InputSchema)["required"])

	opType := asMap(t, byName[ToolEditDocument].InputSchema)["properties"].(map[string]any)["operations"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["type"].(map[string]any)
	assert.ElementsMatch(t, []any{"replace", "insert", "append", "prepend", "delete", "move"}, opType["enum"])
}

func TestSchemaValidationFailuresAreToolErrors(t *testing.T) {
	svc := &fakeService{}
	cs := newFakeSession(t, svc)

	cases := []struct {
		tool string
		args map[string]any
	}{
		{ToolSearch, map[string]any{}},                             // missing query
		{ToolSearch, map[string]any{"query": 42}},                  // wrong type
		{ToolSearch, map[string]any{"query": "x", "limit": 51}},    // above maximum
		{ToolSearch, map[string]any{"query": "x", "position": 1}},  // unknown property
		{ToolGetSummary, map[string]any{}},                         // missing document_id
		{ToolUpdateDocument, map[string]any{"document_id": docID}}, // missing expected_version
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{}}},
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{
			map[string]any{"type": "append", "content": "x", "position": 3}, // positions are never accepted
		}}},
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{
			map[string]any{"type": "rename", "content": "x"},
		}}},
	}
	for _, c := range cases {
		res := call(t, cs, c.tool, c.args)
		assert.True(t, res.IsError, "%s %v must fail", c.tool, c.args)
	}
	assert.Empty(t, svc.calls, "invalid input must never reach the service")
}

func TestDomainValidationReturnsStructuredInvalidInput(t *testing.T) {
	svc := &fakeService{}
	cs := newFakeSession(t, svc)

	cases := []struct {
		tool string
		args map[string]any
		msg  string
	}{
		{ToolSearch, map[string]any{"query": "   "}, "query must not be blank"},
		{ToolGetSummary, map[string]any{"document_id": "not-a-uuid"}, "document_id must be a UUID"},
		{ToolGetSummary, map[string]any{"document_id": "{" + docID + "}"}, "document_id must be a UUID"},
		{ToolCreateDocument, map[string]any{"title": " ", "summary": "s", "parent_ids": []string{docID}}, "title must not be blank"},
		{ToolCreateDocument, map[string]any{"title": "t", "summary": "s", "parent_ids": []string{docID, strings.ToUpper(docID)}}, "distinct"},
		{ToolCreateDocument, map[string]any{"title": "t", "summary": "s", "parent_ids": []string{docID},
			"sources": []any{map[string]any{"kind": "conversation", "source_uri": "https://s3.example.com/x?X-Amz-Signature=abc"}}}, "sources[0]"},
		{ToolUpdateDocument, map[string]any{"document_id": docID, "expected_version": 3}, "at least one of title and summary"},
		{ToolUpdateDocument, map[string]any{"document_id": docID, "expected_version": 3, "summary": "  "}, "summary must not be blank"},
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{
			map[string]any{"type": "insert", "content": "x", "before_block_id": docID, "after_block_id": docID},
		}}, "exactly one of"},
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{
			map[string]any{"type": "append", "content": "ok"},
			map[string]any{"type": "delete", "block_id": docID, "content": ""},
		}}, "operations[1]: delete does not accept content"},
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{
			map[string]any{"type": "replace", "block_id": docID, "content": " \n "},
		}}, "content must not be blank"},
		{ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 1, "operations": []any{
			map[string]any{"type": "move", "block_id": docID, "after_block_id": docID},
		}}, "relative to itself"},
		{ToolAddSource, map[string]any{"document_id": docID, "expected_version": 1, "kind": "conversation", "source_uri": "https://user:pw@example.com/x"}, "credentials"},
		{ToolGetChildren, map[string]any{"document_id": docID, "cursor": "garbage"}, "cursor"},
	}
	for _, c := range cases {
		pub := callErr(t, cs, c.tool, c.args)
		assert.Equal(t, CodeInvalidInput, pub.Code, "%s %v", c.tool, c.args)
		assert.False(t, pub.Retryable)
		assert.Contains(t, pub.Message, c.msg, "%s %v", c.tool, c.args)
	}
	assert.Empty(t, svc.calls, "invalid input must never reach the service")
}

func TestUnexpectedErrorsAreSanitized(t *testing.T) {
	cs := newFakeSession(t, &fakeService{})
	pub := callErr(t, cs, ToolGetSummary, map[string]any{"document_id": docID})
	assert.Equal(t, CodeInternalError, pub.Code)
	assert.NotContains(t, pub.Message, "password")
	assert.NotContains(t, fmt.Sprint(pub.Details), "secret")
	assert.NotEmpty(t, pub.Details["request_id"])
}

func TestVersionConflictCarriesDetails(t *testing.T) {
	id := uuid.MustParse(docID)
	svc := &fakeService{editDoc: func(knowledge.EditDocumentInput) (knowledge.EditDocumentResult, error) {
		return knowledge.EditDocumentResult{}, fmt.Errorf("edit: %w", knowledge.NewVersionConflict(id, 7, 8))
	}}
	cs := newFakeSession(t, svc)
	pub := callErr(t, cs, ToolEditDocument, map[string]any{"document_id": docID, "expected_version": 7,
		"operations": []any{map[string]any{"type": "append", "content": "x"}}})
	assert.Equal(t, CodeVersionConflict, pub.Code)
	assert.True(t, pub.Retryable)
	assert.Equal(t, docID, pub.Details["document_id"])
	assert.EqualValues(t, 7, pub.Details["expected_version"])
	assert.EqualValues(t, 8, pub.Details["current_version"])
}

func TestSuccessfulCallReturnsStructuredContent(t *testing.T) {
	id := uuid.MustParse(docID)
	svc := &fakeService{
		search: func(p knowledge.SearchParams) (knowledge.SearchResponse, error) {
			assert.True(t, p.LexicalFallback)
			assert.Equal(t, 10, p.Limit)
			return knowledge.SearchResponse{
				Query: p.Query,
				Results: []knowledge.SearchResult{{
					ID: id,
					Paths: []knowledge.Path{{
						{ID: knowledge.RootID, Title: "root"},
						{ID: id, Title: "Doc"},
					}},
				}},
				SemanticSearchAvailable: false,
				SemanticError:           errors.New("provider down"),
			}, nil
		},
		getFulltext: func(uuid.UUID, knowledge.BlockPageRequest) (knowledge.FulltextPage, error) {
			return knowledge.FulltextPage{DocumentID: id, Version: 3}, nil
		},
	}
	cs := newFakeSession(t, svc)

	out := callOK[SearchOutput](t, cs, ToolSearch, map[string]any{"query": "  doc  "})
	assert.Equal(t, "doc", out.Query)
	require.Len(t, out.Results, 1)
	assert.Equal(t, []DocumentPath{{{ID: knowledge.RootID.String(), Title: "root"}, {ID: docID, Title: "Doc"}}}, out.Results[0].Paths)
	assert.False(t, out.SemanticSearchAvailable)
	assert.Equal(t, []string{"semantic_search_unavailable"}, out.Warnings)

	// Empty block lists are [] rather than null; next anchor is null.
	res := call(t, cs, ToolGetFulltext, map[string]any{"document_id": docID})
	require.False(t, res.IsError)
	assert.JSONEq(t, `{"document_id":"`+docID+`","version":3,"blocks":[],"has_more":false,"next_after_block_id":null}`, resultText(res))

	// expected_version is compared with the version read with the page.
	pub := callErr(t, cs, ToolGetFulltext, map[string]any{"document_id": docID, "expected_version": 2})
	assert.Equal(t, CodeVersionConflict, pub.Code)
}

func TestRootMetadataIsProtected(t *testing.T) {
	svc := &fakeService{}
	cs := newFakeSession(t, svc)
	pub := callErr(t, cs, ToolUpdateDocument, map[string]any{
		"document_id": knowledge.RootID.String(), "expected_version": 1, "title": "new root",
	})
	assert.Equal(t, CodeRootViolation, pub.Code)
	assert.Empty(t, svc.calls)
}

func TestGetRootMissingIsIntegrityViolation(t *testing.T) {
	svc := &fakeService{getSummary: func(id uuid.UUID) (knowledge.DocumentSummary, error) {
		return knowledge.DocumentSummary{}, fmt.Errorf("%w: document %s", knowledge.ErrNotFound, id)
	}}
	cs := newFakeSession(t, svc)
	pub := callErr(t, cs, ToolGetRoot, map[string]any{})
	assert.Equal(t, CodeIntegrityViolation, pub.Code)
}

func TestErrorMapping(t *testing.T) {
	h := newHandler(&fakeService{}, Options{})
	ctx := context.Background()
	cases := []struct {
		err       error
		code      string
		retryable bool
	}{
		{fmt.Errorf("x: %w", knowledge.ErrInvalidInput), CodeInvalidInput, false},
		{fmt.Errorf("x: %w", knowledge.ErrNotFound), CodeNotFound, false},
		{fmt.Errorf("x: %w", knowledge.ErrRelationshipNotFound), CodeRelationshipNotFound, false},
		{fmt.Errorf("x: %w", knowledge.ErrCycleDetected), CodeCycleDetected, false},
		{fmt.Errorf("x: %w", knowledge.ErrRootViolation), CodeRootViolation, false},
		{fmt.Errorf("x: %w", knowledge.ErrLastParentViolation), CodeLastParentViolation, false},
		{fmt.Errorf("x: %w", knowledge.ErrIntegrityViolation), CodeIntegrityViolation, false},
		{fmt.Errorf("x: %w", knowledge.ErrEmbeddingUnavailable), CodeSemanticSearchUnavailable, true},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), CodeDependencyUnavailable, true},
		{errors.New("boom"), CodeInternalError, true},
	}
	for _, c := range cases {
		pub := h.mapError(ctx, "test", c.err)
		assert.Equal(t, c.code, pub.Code, c.err.Error())
		assert.Equal(t, c.retryable, pub.Retryable, c.err.Error())
		var decoded PublicToolError
		require.NoError(t, json.Unmarshal([]byte(pub.Error()), &decoded))
		assert.Equal(t, pub.Code, decoded.Code)
	}
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}
