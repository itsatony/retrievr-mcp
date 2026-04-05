package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test constants
// ---------------------------------------------------------------------------

const (
	testHFPaperID1   = "2401.12345"
	testHFPaperID2   = "2403.67890"
	testHFTitle1     = "Attention Is All You Need v3"
	testHFTitle2     = "Scaling Laws for Language Models"
	testHFSummary1   = "We present a novel attention mechanism."
	testHFSummary2   = "This paper studies scaling laws."
	testHFAuthor1    = "Alice Researcher"
	testHFAuthor2    = "Bob Scientist"
	testHFDate1      = "2024-01-15T00:00:00.000Z"
	testHFDate2      = "2023-03-20T00:00:00.000Z"
	testHFDate1Short = "2024-01-15"
	testHFDate2Short = "2023-03-20"
	testHFUpvotes1   = 42
	testHFUpvotes2   = 15
	testHFComments1  = 5
	testHFComments2  = 2

	testHFModelID1     = "meta-llama/Llama-3.1-8B"
	testHFModelLikes1  = 2609
	testHFModelDL1     = 68501660
	testHFPipelineTag1 = "text-generation"
	testHFLibrary1     = "transformers"
	testHFModelDate1   = "2024-07-23T00:00:00.000Z"

	testHFDatasetID1     = "rajpurkar/squad_v2"
	testHFDatasetAuthor1 = "rajpurkar"
	testHFDatasetLikes1  = 244
	testHFDatasetDL1     = 33067
	testHFDatasetDesc1   = "Stanford Question Answering Dataset v2"
	testHFDatasetDate1   = "2022-03-02T23:29:22.000Z"
	testHFDatasetTag1    = "task_categories:question-answering"
	testHFDatasetTag2    = "language:en"

	testHFPluginTimeout        = 5 * time.Second
	testHFConcurrentGoroutines = 10
	testHFAPIKey               = "hf_test_token_abc123"
	testHFPerCallKey           = "hf_per_call_key_xyz"

	testHFMarkdownContent = "# Attention Is All You Need v3\n\nWe present a novel attention mechanism."

	testHFModelTag1 = "pytorch"
	testHFModelTag2 = "text-generation"
)

// ---------------------------------------------------------------------------
// JSON fixture builder types
// ---------------------------------------------------------------------------

type hfTestPaper struct {
	ID          string
	Title       string
	Summary     string
	PublishedAt string
	Upvotes     int
	AuthorNames []string
	NumComments int
}

type hfTestModel struct {
	ID          string
	ModelID     string
	Likes       int
	Downloads   int
	PipelineTag string
	LibraryName string
	Tags        []string
	CreatedAt   string
	Private     bool
}

type hfTestDataset struct {
	ID          string
	Author      string
	Likes       int
	Downloads   int
	Tags        []string
	CreatedAt   string
	Description string
	Private     bool
}

// ---------------------------------------------------------------------------
// JSON fixture builders
// ---------------------------------------------------------------------------

func buildHFTestPapersSearchJSON(papers []hfTestPaper) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, p := range papers {
		if i > 0 {
			sb.WriteString(",")
		}
		authors := buildHFTestAuthorsJSON(p.AuthorNames)
		fmt.Fprintf(&sb, `{
			"paper": {
				"id": %q,
				"title": %q,
				"summary": %q,
				"publishedAt": %q,
				"upvotes": %d,
				"authors": %s
			},
			"publishedAt": %q,
			"title": %q,
			"summary": %q,
			"numComments": %d
		}`, p.ID, p.Title, p.Summary, p.PublishedAt, p.Upvotes, authors,
			p.PublishedAt, p.Title, p.Summary, p.NumComments)
	}
	sb.WriteString("]")
	return sb.String()
}

func buildHFTestPaperGetJSON(p hfTestPaper) string {
	authors := buildHFTestAuthorsJSON(p.AuthorNames)
	return fmt.Sprintf(`{
		"id": %q,
		"title": %q,
		"summary": %q,
		"publishedAt": %q,
		"upvotes": %d,
		"authors": %s
	}`, p.ID, p.Title, p.Summary, p.PublishedAt, p.Upvotes, authors)
}

func buildHFTestAuthorsJSON(names []string) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i, name := range names {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name": %q, "user": null}`, name)
	}
	sb.WriteString("]")
	return sb.String()
}

func buildHFTestModelsSearchJSON(models []hfTestModel) string {
	items := make([]string, 0, len(models))
	for _, m := range models {
		items = append(items, buildHFTestModelJSON(m))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func buildHFTestModelJSON(m hfTestModel) string {
	tagsJSON, _ := json.Marshal(m.Tags)
	return fmt.Sprintf(`{
		"id": %q,
		"modelId": %q,
		"likes": %d,
		"downloads": %d,
		"pipeline_tag": %q,
		"library_name": %q,
		"tags": %s,
		"createdAt": %q,
		"private": %v
	}`, m.ID, m.ModelID, m.Likes, m.Downloads, m.PipelineTag, m.LibraryName,
		string(tagsJSON), m.CreatedAt, m.Private)
}

func buildHFTestDatasetsSearchJSON(datasets []hfTestDataset) string {
	items := make([]string, 0, len(datasets))
	for _, d := range datasets {
		items = append(items, buildHFTestDatasetJSON(d))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func buildHFTestDatasetJSON(d hfTestDataset) string {
	tagsJSON, _ := json.Marshal(d.Tags)
	return fmt.Sprintf(`{
		"id": %q,
		"author": %q,
		"likes": %d,
		"downloads": %d,
		"tags": %s,
		"createdAt": %q,
		"description": %q,
		"private": %v
	}`, d.ID, d.Author, d.Likes, d.Downloads, string(tagsJSON),
		d.CreatedAt, d.Description, d.Private)
}

// ---------------------------------------------------------------------------
// Default test fixtures
// ---------------------------------------------------------------------------

func defaultHFTestPaper1() hfTestPaper {
	return hfTestPaper{
		ID:          testHFPaperID1,
		Title:       testHFTitle1,
		Summary:     testHFSummary1,
		PublishedAt: testHFDate1,
		Upvotes:     testHFUpvotes1,
		AuthorNames: []string{testHFAuthor1, testHFAuthor2},
		NumComments: testHFComments1,
	}
}

func defaultHFTestPaper2() hfTestPaper {
	return hfTestPaper{
		ID:          testHFPaperID2,
		Title:       testHFTitle2,
		Summary:     testHFSummary2,
		PublishedAt: testHFDate2,
		Upvotes:     testHFUpvotes2,
		AuthorNames: []string{testHFAuthor2},
		NumComments: testHFComments2,
	}
}

func defaultHFTestModel1() hfTestModel {
	return hfTestModel{
		ID:          testHFModelID1,
		ModelID:     testHFModelID1,
		Likes:       testHFModelLikes1,
		Downloads:   testHFModelDL1,
		PipelineTag: testHFPipelineTag1,
		LibraryName: testHFLibrary1,
		Tags:        []string{testHFModelTag1, testHFModelTag2},
		CreatedAt:   testHFModelDate1,
		Private:     false,
	}
}

func defaultHFTestDataset1() hfTestDataset {
	return hfTestDataset{
		ID:          testHFDatasetID1,
		Author:      testHFDatasetAuthor1,
		Likes:       testHFDatasetLikes1,
		Downloads:   testHFDatasetDL1,
		Tags:        []string{testHFDatasetTag1, testHFDatasetTag2},
		CreatedAt:   testHFDatasetDate1,
		Description: testHFDatasetDesc1,
		Private:     false,
	}
}

// ---------------------------------------------------------------------------
// Test helper: plugin construction
// ---------------------------------------------------------------------------

func newHFTestPlugin(t *testing.T, baseURL string) *HuggingFacePlugin {
	t.Helper()
	return newHFTestPluginWithConfig(t, baseURL, true, true, true)
}

func newHFTestPluginWithConfig(t *testing.T, baseURL string, papers, models, datasets bool) *HuggingFacePlugin {
	t.Helper()

	extra := map[string]string{
		hfExtraKeyIncludePapers:   fmt.Sprintf("%v", papers),
		hfExtraKeyIncludeModels:   fmt.Sprintf("%v", models),
		hfExtraKeyIncludeDatasets: fmt.Sprintf("%v", datasets),
	}

	plugin := &HuggingFacePlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testHFPluginTimeout},
		RateLimit: 10.0,
		Extra:     extra,
	})
	require.NoError(t, err)
	return plugin
}

func newHFTestPluginWithAPIKey(t *testing.T, baseURL, apiKey string) *HuggingFacePlugin {
	t.Helper()

	plugin := &HuggingFacePlugin{}
	err := plugin.Initialize(context.Background(), PluginConfig{
		Enabled:   true,
		APIKey:    apiKey,
		BaseURL:   baseURL,
		Timeout:   Duration{Duration: testHFPluginTimeout},
		RateLimit: 10.0,
		Extra: map[string]string{
			hfExtraKeyIncludePapers:   hfExtraValueTrue,
			hfExtraKeyIncludeModels:   hfExtraValueTrue,
			hfExtraKeyIncludeDatasets: hfExtraValueTrue,
		},
	})
	require.NoError(t, err)
	return plugin
}

// ---------------------------------------------------------------------------
// Test: Contract
// ---------------------------------------------------------------------------

func TestHuggingFacePluginContract(t *testing.T) {
	t.Parallel()
	plugin := newHFTestPlugin(t, "http://unused.test/")
	PluginContractTest(t, plugin)
}

// ---------------------------------------------------------------------------
// Test: Search — Papers
// ---------------------------------------------------------------------------

func TestHuggingFaceSearch(t *testing.T) {
	t.Parallel()

	t.Run("basic_paper_search", func(t *testing.T) {
		t.Parallel()
		papers := []hfTestPaper{defaultHFTestPaper1(), defaultHFTestPaper2()}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIPapersSearchPath, r.URL.Path)
			assert.Equal(t, "attention", r.URL.Query().Get(hfParamQuery))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestPapersSearchJSON(papers))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "attention",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Results, 2)
		assert.Equal(t, ContentTypePaper, result.Results[0].ContentType)
	})

	t.Run("basic_model_search", func(t *testing.T) {
		t.Parallel()
		models := []hfTestModel{defaultHFTestModel1()}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIModelsPath, r.URL.Path)
			assert.Equal(t, "llama", r.URL.Query().Get(hfParamSearch))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestModelsSearchJSON(models))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, true, false)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "llama",
			ContentType: ContentTypeModel,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Results, 1)
		assert.Equal(t, ContentTypeModel, result.Results[0].ContentType)
	})

	t.Run("basic_dataset_search", func(t *testing.T) {
		t.Parallel()
		datasets := []hfTestDataset{defaultHFTestDataset1()}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIDatasetsPath, r.URL.Path)
			assert.Equal(t, "squad", r.URL.Query().Get(hfParamSearch))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestDatasetsSearchJSON(datasets))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, false, true)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "squad",
			ContentType: ContentTypeDataset,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Results, 1)
		assert.Equal(t, ContentTypeDataset, result.Results[0].ContentType)
	})

	t.Run("search_any_content_type", func(t *testing.T) {
		t.Parallel()
		var papersHit, modelsHit, datasetsHit atomic.Bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case hfAPIPapersSearchPath:
				papersHit.Store(true)
				fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
			case hfAPIModelsPath:
				modelsHit.Store(true)
				fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
			case hfAPIDatasetsPath:
				datasetsHit.Store(true)
				fmt.Fprint(w, buildHFTestDatasetsSearchJSON([]hfTestDataset{defaultHFTestDataset1()}))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "transformer",
			ContentType: ContentTypeAny,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Results, 3)
		assert.True(t, papersHit.Load(), "papers API should be hit")
		assert.True(t, modelsHit.Load(), "models API should be hit")
		assert.True(t, datasetsHit.Load(), "datasets API should be hit")
	})

	t.Run("search_with_category_filter", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIModelsPath, r.URL.Path)
			assert.Contains(t, r.URL.Query().Get(hfParamFilter), testHFPipelineTag1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, true, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "llm",
			ContentType: ContentTypeModel,
			Limit:       10,
			Filters:     SearchFilters{Categories: []string{testHFPipelineTag1}},
		}, nil)
		require.NoError(t, err)
	})

	t.Run("search_with_sort_date", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfSortCreatedAt, r.URL.Query().Get(hfParamSort))
			assert.Equal(t, hfDirectionDesc, r.URL.Query().Get(hfParamDirection))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, true, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "model",
			ContentType: ContentTypeModel,
			Sort:        SortDateDesc,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
	})

	t.Run("search_with_sort_citations_maps_to_downloads", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfSortDownloads, r.URL.Query().Get(hfParamSort))
			assert.Equal(t, hfDirectionDesc, r.URL.Query().Get(hfParamDirection))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, true, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "model",
			ContentType: ContentTypeModel,
			Sort:        SortCitations,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
	})

	t.Run("search_with_pagination_papers_skip", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "20", r.URL.Query().Get(hfParamSkip))
			assert.Equal(t, "10", r.URL.Query().Get(hfParamLimit))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
			Offset:      20,
		}, nil)
		require.NoError(t, err)
	})

	t.Run("search_with_pagination_models_offset", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "20", r.URL.Query().Get(hfParamOffset))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, true, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeModel,
			Limit:       10,
			Offset:      20,
		}, nil)
		require.NoError(t, err)
	})

	t.Run("search_empty_query_error", func(t *testing.T) {
		t.Parallel()
		plugin := newHFTestPlugin(t, "http://unused.test/")
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrHFEmptyQuery))
	})

	t.Run("search_no_results", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "nonexistent",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
		assert.Empty(t, result.Results)
		assert.False(t, result.HasMore)
	})

	t.Run("search_dataset_sort_date_maps_to_lastModified", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfSortLastModified, r.URL.Query().Get(hfParamSort))
			assert.Equal(t, hfDirectionDesc, r.URL.Query().Get(hfParamDirection))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "[]")
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, false, true)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeDataset,
			Sort:        SortDateDesc,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Test: Search result mapping
// ---------------------------------------------------------------------------

func TestHuggingFaceSearchPaperMapping(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
	}))
	defer server.Close()

	plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
	result, err := plugin.Search(context.Background(), SearchParams{
		Query:       "test",
		ContentType: ContentTypePaper,
		Limit:       10,
	}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceHuggingFace+prefixedIDSeparator+hfSubTypePaper+testHFPaperID1, pub.ID)
	assert.Equal(t, SourceHuggingFace, pub.Source)
	assert.Equal(t, ContentTypePaper, pub.ContentType)
	assert.Equal(t, testHFTitle1, pub.Title)
	assert.Equal(t, testHFSummary1, pub.Abstract)
	assert.Equal(t, testHFPaperID1, pub.ArXivID)
	assert.Equal(t, hfPaperURLPrefix+testHFPaperID1, pub.URL)
	assert.Equal(t, testHFDate1Short, pub.Published)
	require.Len(t, pub.Authors, 2)
	assert.Equal(t, testHFAuthor1, pub.Authors[0].Name)
	assert.Equal(t, testHFAuthor2, pub.Authors[1].Name)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testHFUpvotes1, pub.SourceMetadata[hfMetaKeyUpvotes])
	assert.Equal(t, testHFComments1, pub.SourceMetadata[hfMetaKeyNumComments])
}

func TestHuggingFaceSearchModelMapping(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
	}))
	defer server.Close()

	plugin := newHFTestPluginWithConfig(t, server.URL, false, true, false)
	result, err := plugin.Search(context.Background(), SearchParams{
		Query:       "test",
		ContentType: ContentTypeModel,
		Limit:       10,
	}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceHuggingFace+prefixedIDSeparator+hfSubTypeModel+testHFModelID1, pub.ID)
	assert.Equal(t, SourceHuggingFace, pub.Source)
	assert.Equal(t, ContentTypeModel, pub.ContentType)
	assert.Equal(t, testHFModelID1, pub.Title)
	assert.Equal(t, hfModelURLPrefix+testHFModelID1, pub.URL)
	assert.Equal(t, "2024-07-23", pub.Published)
	assert.Contains(t, pub.Categories, testHFModelTag1)
	assert.Contains(t, pub.Categories, testHFModelTag2)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testHFModelDL1, pub.SourceMetadata[hfMetaKeyDownloads])
	assert.Equal(t, testHFModelLikes1, pub.SourceMetadata[hfMetaKeyLikes])
	assert.Equal(t, testHFPipelineTag1, pub.SourceMetadata[hfMetaKeyPipelineTag])
	assert.Equal(t, testHFLibrary1, pub.SourceMetadata[hfMetaKeyLibraryName])
}

func TestHuggingFaceSearchDatasetMapping(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildHFTestDatasetsSearchJSON([]hfTestDataset{defaultHFTestDataset1()}))
	}))
	defer server.Close()

	plugin := newHFTestPluginWithConfig(t, server.URL, false, false, true)
	result, err := plugin.Search(context.Background(), SearchParams{
		Query:       "test",
		ContentType: ContentTypeDataset,
		Limit:       10,
	}, nil)

	require.NoError(t, err)
	require.Len(t, result.Results, 1)

	pub := result.Results[0]
	assert.Equal(t, SourceHuggingFace+prefixedIDSeparator+hfSubTypeDataset+testHFDatasetID1, pub.ID)
	assert.Equal(t, SourceHuggingFace, pub.Source)
	assert.Equal(t, ContentTypeDataset, pub.ContentType)
	assert.Equal(t, testHFDatasetID1, pub.Title)
	assert.Equal(t, testHFDatasetDesc1, pub.Abstract)
	assert.Equal(t, hfDatasetURLPrefix+testHFDatasetID1, pub.URL)
	assert.Equal(t, "2022-03-02", pub.Published)

	// Source metadata.
	require.NotNil(t, pub.SourceMetadata)
	assert.Equal(t, testHFDatasetDL1, pub.SourceMetadata[hfMetaKeyDownloads])
	assert.Equal(t, testHFDatasetLikes1, pub.SourceMetadata[hfMetaKeyLikes])
	assert.Equal(t, testHFDatasetAuthor1, pub.SourceMetadata[hfMetaKeyAuthor])
}

// ---------------------------------------------------------------------------
// Test: Content type routing
// ---------------------------------------------------------------------------

func TestHuggingFaceContentTypeRouting(t *testing.T) {
	t.Parallel()

	t.Run("papers_disabled_paper_content_type_returns_empty", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("no API should be hit when papers are disabled for paper content type")
			http.NotFound(w, nil)
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, false, true, true)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		assert.Empty(t, result.Results)
	})

	t.Run("models_disabled_model_content_type_returns_empty", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("no API should be hit when models are disabled for model content type")
			http.NotFound(w, nil)
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, true)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeModel,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		assert.Empty(t, result.Results)
	})

	t.Run("datasets_disabled_dataset_content_type_returns_empty", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Error("no API should be hit when datasets are disabled for dataset content type")
			http.NotFound(w, nil)
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, true, false)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeDataset,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		assert.Empty(t, result.Results)
	})

	t.Run("any_content_type_only_hits_enabled_sub_sources", func(t *testing.T) {
		t.Parallel()
		var papersHit, modelsHit, datasetsHit atomic.Bool

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case hfAPIPapersSearchPath:
				papersHit.Store(true)
				fmt.Fprint(w, "[]")
			case hfAPIModelsPath:
				modelsHit.Store(true)
				fmt.Fprint(w, "[]")
			case hfAPIDatasetsPath:
				datasetsHit.Store(true)
				fmt.Fprint(w, "[]")
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		// Only papers and models enabled.
		plugin := newHFTestPluginWithConfig(t, server.URL, true, true, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeAny,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		assert.True(t, papersHit.Load(), "papers API should be hit")
		assert.True(t, modelsHit.Load(), "models API should be hit")
		assert.False(t, datasetsHit.Load(), "datasets API should NOT be hit")
	})
}

// ---------------------------------------------------------------------------
// Test: Search partial failure
// ---------------------------------------------------------------------------

func TestHuggingFaceSearchPartialFailure(t *testing.T) {
	t.Parallel()

	t.Run("one_sub_api_fails_others_succeed", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case hfAPIPapersSearchPath:
				w.WriteHeader(http.StatusInternalServerError)
			case hfAPIModelsPath:
				fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
			case hfAPIDatasetsPath:
				fmt.Fprint(w, buildHFTestDatasetsSearchJSON([]hfTestDataset{defaultHFTestDataset1()}))
			}
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		result, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeAny,
			Limit:       10,
		}, nil)

		require.NoError(t, err)
		assert.Len(t, result.Results, 2)
	})

	t.Run("all_sub_apis_fail", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypeAny,
			Limit:       10,
		}, nil)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSearchFailed))
	})
}

// ---------------------------------------------------------------------------
// Test: Get
// ---------------------------------------------------------------------------

func TestHuggingFaceGet(t *testing.T) {
	t.Parallel()

	t.Run("get_paper_native", func(t *testing.T) {
		t.Parallel()
		paper := defaultHFTestPaper1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIPaperGetPath+testHFPaperID1, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestPaperGetJSON(paper))
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		pub, err := plugin.Get(context.Background(), hfSubTypePaper+testHFPaperID1, nil, FormatNative, nil)

		require.NoError(t, err)
		require.NotNil(t, pub)
		assert.Equal(t, testHFTitle1, pub.Title)
		assert.Equal(t, testHFPaperID1, pub.ArXivID)
		assert.Equal(t, ContentTypePaper, pub.ContentType)
	})

	t.Run("get_paper_with_full_text", func(t *testing.T) {
		t.Parallel()
		paper := defaultHFTestPaper1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case hfAPIPaperGetPath + testHFPaperID1:
				fmt.Fprint(w, buildHFTestPaperGetJSON(paper))
			case hfAPIPaperMarkdownPath + testHFPaperID1 + hfPaperMDSuffix:
				w.Header().Set("Content-Type", "text/markdown")
				fmt.Fprint(w, testHFMarkdownContent)
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		pub, err := plugin.Get(context.Background(), hfSubTypePaper+testHFPaperID1,
			[]IncludeField{IncludeFullText}, FormatNative, nil)

		require.NoError(t, err)
		require.NotNil(t, pub)
		require.NotNil(t, pub.FullText)
		assert.Equal(t, testHFMarkdownContent, pub.FullText.Content)
		assert.Equal(t, FormatMarkdown, pub.FullText.ContentFormat)
		assert.Equal(t, len(testHFMarkdownContent), pub.FullText.ContentLength)
	})

	t.Run("get_paper_with_related_models", func(t *testing.T) {
		t.Parallel()
		paper := defaultHFTestPaper1()
		models := []hfTestModel{defaultHFTestModel1()}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case hfAPIPaperGetPath + testHFPaperID1:
				fmt.Fprint(w, buildHFTestPaperGetJSON(paper))
			case hfAPIModelsPath:
				assert.Contains(t, r.URL.Query().Get(hfParamFilter), hfArxivFilterPrefix+testHFPaperID1)
				fmt.Fprint(w, buildHFTestModelsSearchJSON(models))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		pub, err := plugin.Get(context.Background(), hfSubTypePaper+testHFPaperID1,
			[]IncludeField{IncludeRelated}, FormatNative, nil)

		require.NoError(t, err)
		require.NotNil(t, pub)
		require.Len(t, pub.Related, 1)
		assert.Contains(t, pub.Related[0].ID, testHFModelID1)
		assert.Equal(t, testHFModelID1, pub.Related[0].Title)

		// Check linked models in metadata.
		linkedModels, ok := pub.SourceMetadata[hfMetaKeyLinkedModels]
		require.True(t, ok)
		modelIDs, ok := linkedModels.([]string)
		require.True(t, ok)
		assert.Contains(t, modelIDs, testHFModelID1)
	})

	t.Run("get_paper_bibtex_unsupported", func(t *testing.T) {
		t.Parallel()
		paper := defaultHFTestPaper1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestPaperGetJSON(paper))
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.Get(context.Background(), hfSubTypePaper+testHFPaperID1, nil, FormatBibTeX, nil)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrFormatUnsupported))
	})

	t.Run("get_model_native", func(t *testing.T) {
		t.Parallel()
		model := defaultHFTestModel1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIModelsSlashPath+testHFModelID1, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestModelJSON(model))
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		pub, err := plugin.Get(context.Background(), hfSubTypeModel+testHFModelID1, nil, FormatNative, nil)

		require.NoError(t, err)
		require.NotNil(t, pub)
		assert.Equal(t, testHFModelID1, pub.Title)
		assert.Equal(t, ContentTypeModel, pub.ContentType)
		assert.Equal(t, testHFModelDL1, pub.SourceMetadata[hfMetaKeyDownloads])
	})

	t.Run("get_dataset_native", func(t *testing.T) {
		t.Parallel()
		dataset := defaultHFTestDataset1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAPIDatasetsSlashPath+testHFDatasetID1, r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestDatasetJSON(dataset))
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		pub, err := plugin.Get(context.Background(), hfSubTypeDataset+testHFDatasetID1, nil, FormatNative, nil)

		require.NoError(t, err)
		require.NotNil(t, pub)
		assert.Equal(t, testHFDatasetID1, pub.Title)
		assert.Equal(t, ContentTypeDataset, pub.ContentType)
		assert.Equal(t, testHFDatasetDesc1, pub.Abstract)
	})

	t.Run("get_not_found", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.Get(context.Background(), hfSubTypePaper+"9999.99999", nil, FormatNative, nil)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrGetFailed))
	})

	t.Run("get_unknown_sub_type", func(t *testing.T) {
		t.Parallel()

		plugin := newHFTestPlugin(t, "http://unused.test/")
		_, err := plugin.Get(context.Background(), "unknown/foo", nil, FormatNative, nil)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrHFNotFound))
	})

	t.Run("get_model_unsupported_format_xml", func(t *testing.T) {
		t.Parallel()
		model := defaultHFTestModel1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestModelJSON(model))
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.Get(context.Background(), hfSubTypeModel+testHFModelID1, nil, FormatXML, nil)

		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrFormatUnsupported))
	})

	t.Run("get_paper_full_text_failure_non_fatal", func(t *testing.T) {
		t.Parallel()
		paper := defaultHFTestPaper1()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case hfAPIPaperGetPath + testHFPaperID1:
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, buildHFTestPaperGetJSON(paper))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		pub, err := plugin.Get(context.Background(), hfSubTypePaper+testHFPaperID1,
			[]IncludeField{IncludeFullText}, FormatNative, nil)

		require.NoError(t, err)
		require.NotNil(t, pub)
		assert.Nil(t, pub.FullText) // Non-fatal — no full text on failure.
	})
}

// ---------------------------------------------------------------------------
// Test: Credential resolution
// ---------------------------------------------------------------------------

func TestHuggingFaceCredentialResolution(t *testing.T) {
	t.Parallel()

	t.Run("per_call_overrides_server", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAuthBearerPrefix+testHFPerCallKey, r.Header.Get(hfAuthHeader))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithAPIKey(t, server.URL, testHFAPIKey)
		creds := &CallCredentials{HFToken: testHFPerCallKey}
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, creds)
		require.NoError(t, err)
	})

	t.Run("server_default_fallback", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, hfAuthBearerPrefix+testHFAPIKey, r.Header.Get(hfAuthHeader))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
		}))
		defer server.Close()

		plugin := newHFTestPluginWithAPIKey(t, server.URL, testHFAPIKey)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
	})

	t.Run("anonymous_no_auth_header", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Empty(t, r.Header.Get(hfAuthHeader))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// Test: Health tracking
// ---------------------------------------------------------------------------

func TestHuggingFaceHealthTracking(t *testing.T) {
	t.Parallel()

	t.Run("initially_healthy", func(t *testing.T) {
		t.Parallel()
		plugin := newHFTestPlugin(t, "http://unused.test/")
		health := plugin.Health(context.Background())
		assert.True(t, health.Healthy)
		assert.Empty(t, health.LastError)
	})

	t.Run("failure_marks_unhealthy", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		_, _ = plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)

		health := plugin.Health(context.Background())
		assert.False(t, health.Healthy)
		assert.NotEmpty(t, health.LastError)
	})

	t.Run("success_recovers_health", func(t *testing.T) {
		t.Parallel()
		var callCount atomic.Int32

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if callCount.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, "[]")
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)

		// First call fails.
		_, _ = plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		assert.False(t, plugin.Health(context.Background()).Healthy)

		// Second call succeeds.
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.NoError(t, err)
		assert.True(t, plugin.Health(context.Background()).Healthy)
	})
}

// ---------------------------------------------------------------------------
// Test: HTTP errors
// ---------------------------------------------------------------------------

func TestHuggingFaceHTTPErrors(t *testing.T) {
	t.Parallel()

	t.Run("server_error_500", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSearchFailed))
	})

	t.Run("not_found_404_get", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.Get(context.Background(), hfSubTypePaper+"9999.99999", nil, FormatNative, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrGetFailed))
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testHFPluginTimeout)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately.

		_, err := plugin.Search(ctx, SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.Error(t, err)
	})

	t.Run("malformed_json", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{invalid json")
		}))
		defer server.Close()

		plugin := newHFTestPluginWithConfig(t, server.URL, true, false, false)
		_, err := plugin.Search(context.Background(), SearchParams{
			Query:       "test",
			ContentType: ContentTypePaper,
			Limit:       10,
		}, nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSearchFailed))
	})
}

// ---------------------------------------------------------------------------
// Test: Initialize
// ---------------------------------------------------------------------------

func TestHuggingFaceInitialize(t *testing.T) {
	t.Parallel()

	t.Run("default_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, hfDefaultBaseURL, plugin.baseURL)
	})

	t.Run("custom_base_url", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			BaseURL: "http://custom.test",
		})
		require.NoError(t, err)
		assert.Equal(t, "http://custom.test", plugin.baseURL)
	})

	t.Run("default_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, DefaultPluginTimeout, plugin.httpClient.Timeout)
	})

	t.Run("custom_timeout", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Timeout: Duration{Duration: 30 * time.Second},
		})
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, plugin.httpClient.Timeout)
	})

	t.Run("api_key_stored", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			APIKey:  testHFAPIKey,
		})
		require.NoError(t, err)
		assert.Equal(t, testHFAPIKey, plugin.apiKey)
	})

	t.Run("rate_limit_reported", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled:   true,
			RateLimit: 15.0,
		})
		require.NoError(t, err)
		health := plugin.Health(context.Background())
		assert.Equal(t, 15.0, health.RateLimit)
	})

	t.Run("extras_parsed", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{
			Enabled: true,
			Extra: map[string]string{
				hfExtraKeyIncludePapers:   hfExtraValueTrue,
				hfExtraKeyIncludeModels:   "false",
				hfExtraKeyIncludeDatasets: hfExtraValueTrue,
			},
		})
		require.NoError(t, err)
		assert.True(t, plugin.includePapers)
		assert.False(t, plugin.includeModels)
		assert.True(t, plugin.includeDatasets)
	})

	t.Run("no_extras_defaults_all_enabled", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.True(t, plugin.includePapers)
		assert.True(t, plugin.includeModels)
		assert.True(t, plugin.includeDatasets)
	})

	t.Run("initially_healthy", func(t *testing.T) {
		t.Parallel()
		plugin := &HuggingFacePlugin{}
		err := plugin.Initialize(context.Background(), PluginConfig{Enabled: true})
		require.NoError(t, err)
		assert.True(t, plugin.healthy)
	})
}

// ---------------------------------------------------------------------------
// Test: Concurrent access
// ---------------------------------------------------------------------------

func TestHuggingFaceConcurrentAccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case hfAPIPapersSearchPath:
			fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
		case hfAPIModelsPath:
			fmt.Fprint(w, buildHFTestModelsSearchJSON([]hfTestModel{defaultHFTestModel1()}))
		case hfAPIDatasetsPath:
			fmt.Fprint(w, buildHFTestDatasetsSearchJSON([]hfTestDataset{defaultHFTestDataset1()}))
		default:
			// Handle Get requests.
			if strings.HasPrefix(r.URL.Path, hfAPIPaperGetPath) {
				fmt.Fprint(w, buildHFTestPaperGetJSON(defaultHFTestPaper1()))
			} else if strings.HasPrefix(r.URL.Path, hfAPIModelsSlashPath) {
				fmt.Fprint(w, buildHFTestModelJSON(defaultHFTestModel1()))
			} else {
				http.NotFound(w, r)
			}
		}
	}))
	defer server.Close()

	plugin := newHFTestPlugin(t, server.URL)

	var wg sync.WaitGroup
	for i := range testHFConcurrentGoroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			switch idx % 4 {
			case 0:
				_, _ = plugin.Search(context.Background(), SearchParams{
					Query:       "test",
					ContentType: ContentTypePaper,
					Limit:       10,
				}, nil)
			case 1:
				_, _ = plugin.Search(context.Background(), SearchParams{
					Query:       "test",
					ContentType: ContentTypeModel,
					Limit:       10,
				}, nil)
			case 2:
				_, _ = plugin.Get(context.Background(), hfSubTypePaper+testHFPaperID1, nil, FormatNative, nil)
			case 3:
				_ = plugin.Health(context.Background())
			}
		}(i)
	}

	wg.Wait()
	// No panics or data races (verified by -race flag).
}

// ---------------------------------------------------------------------------
// Test: Date parsing
// ---------------------------------------------------------------------------

func TestHFParseDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"valid_iso", testHFDate1, testHFDate1Short},
		{"valid_iso_2", testHFDate2, testHFDate2Short},
		{"empty_string", "", ""},
		{"invalid_format", "2024-01-15", ""},
		{"garbage", "not-a-date", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, parseHFDate(tc.input))
		})
	}
}

// ---------------------------------------------------------------------------
// Test: URL builders
// ---------------------------------------------------------------------------

func TestBuildHFPapersSearchURL(t *testing.T) {
	t.Parallel()

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		result := buildHFPapersSearchURL("http://test.com", SearchParams{
			Query: "attention",
			Limit: 10,
		})
		assert.Contains(t, result, hfAPIPapersSearchPath)
		assert.Contains(t, result, hfParamQuery+"=attention")
		assert.Contains(t, result, hfParamLimit+"=10")
	})

	t.Run("with_offset", func(t *testing.T) {
		t.Parallel()
		result := buildHFPapersSearchURL("http://test.com", SearchParams{
			Query:  "test",
			Limit:  5,
			Offset: 20,
		})
		assert.Contains(t, result, hfParamSkip+"=20")
	})

	t.Run("caps_limit_at_max", func(t *testing.T) {
		t.Parallel()
		result := buildHFPapersSearchURL("http://test.com", SearchParams{
			Query: "test",
			Limit: 500,
		})
		assert.Contains(t, result, hfParamLimit+"=100")
	})
}

func TestBuildHFModelsSearchURL(t *testing.T) {
	t.Parallel()

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		result := buildHFModelsSearchURL("http://test.com", SearchParams{
			Query: "llama",
			Limit: 10,
		})
		assert.Contains(t, result, hfAPIModelsPath)
		assert.Contains(t, result, hfParamSearch+"=llama")
		assert.Contains(t, result, hfParamLimit+"=10")
	})

	t.Run("with_sort", func(t *testing.T) {
		t.Parallel()
		result := buildHFModelsSearchURL("http://test.com", SearchParams{
			Query: "test",
			Limit: 10,
			Sort:  SortDateDesc,
		})
		assert.Contains(t, result, hfParamSort+"="+hfSortCreatedAt)
		assert.Contains(t, result, hfParamDirection+"="+hfDirectionDesc)
	})

	t.Run("with_categories", func(t *testing.T) {
		t.Parallel()
		result := buildHFModelsSearchURL("http://test.com", SearchParams{
			Query:   "test",
			Limit:   10,
			Filters: SearchFilters{Categories: []string{testHFPipelineTag1}},
		})
		assert.Contains(t, result, hfParamFilter+"="+testHFPipelineTag1)
	})
}

func TestBuildHFDatasetsSearchURL(t *testing.T) {
	t.Parallel()

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		result := buildHFDatasetsSearchURL("http://test.com", SearchParams{
			Query: "squad",
			Limit: 10,
		})
		assert.Contains(t, result, hfAPIDatasetsPath)
		assert.Contains(t, result, hfParamSearch+"=squad")
	})

	t.Run("date_sort_maps_to_lastModified", func(t *testing.T) {
		t.Parallel()
		result := buildHFDatasetsSearchURL("http://test.com", SearchParams{
			Query: "test",
			Limit: 10,
			Sort:  SortDateDesc,
		})
		assert.Contains(t, result, hfParamSort+"="+hfSortLastModified)
	})
}

func TestBuildHFGetURLs(t *testing.T) {
	t.Parallel()

	t.Run("paper_get", func(t *testing.T) {
		t.Parallel()
		result := buildHFPaperGetURL("http://test.com", testHFPaperID1)
		assert.Equal(t, "http://test.com"+hfAPIPaperGetPath+testHFPaperID1, result)
	})

	t.Run("paper_markdown", func(t *testing.T) {
		t.Parallel()
		result := buildHFPaperMarkdownURL("http://test.com", testHFPaperID1)
		assert.Equal(t, "http://test.com"+hfAPIPaperMarkdownPath+testHFPaperID1+hfPaperMDSuffix, result)
	})

	t.Run("paper_linked_models", func(t *testing.T) {
		t.Parallel()
		result := buildHFPaperLinkedModelsURL("http://test.com", testHFPaperID1)
		assert.Contains(t, result, hfAPIModelsPath)
		assert.Contains(t, result, testHFPaperID1)
		assert.Contains(t, result, hfParamFilter)
	})

	t.Run("model_get", func(t *testing.T) {
		t.Parallel()
		result := buildHFModelGetURL("http://test.com", testHFModelID1)
		assert.Equal(t, "http://test.com"+hfAPIModelsSlashPath+testHFModelID1, result)
	})

	t.Run("dataset_get", func(t *testing.T) {
		t.Parallel()
		result := buildHFDatasetGetURL("http://test.com", testHFDatasetID1)
		assert.Equal(t, "http://test.com"+hfAPIDatasetsSlashPath+testHFDatasetID1, result)
	})
}

// ---------------------------------------------------------------------------
// Test: JSON parsing edge cases
// ---------------------------------------------------------------------------

func TestHuggingFaceJSONParsingEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("nil_authors", func(t *testing.T) {
		t.Parallel()
		wrapper := &hfPaperSearchResult{
			Paper: hfPaper{
				ID:      testHFPaperID1,
				Title:   testHFTitle1,
				Authors: nil,
			},
		}
		pub := mapHFPaperToPublication(wrapper)
		assert.Nil(t, pub.Authors)
	})

	t.Run("empty_tags_model", func(t *testing.T) {
		t.Parallel()
		model := &hfModel{
			ID:   testHFModelID1,
			Tags: nil,
		}
		pub := mapHFModelToPublication(model)
		assert.Nil(t, pub.Categories)
	})

	t.Run("empty_description_dataset", func(t *testing.T) {
		t.Parallel()
		dataset := &hfDataset{
			ID:          testHFDatasetID1,
			Description: "",
		}
		pub := mapHFDatasetToPublication(dataset)
		assert.Empty(t, pub.Abstract)
	})

	t.Run("author_with_no_name", func(t *testing.T) {
		t.Parallel()
		authors := mapHFAuthors([]hfAuthor{{Name: ""}, {Name: testHFAuthor1}})
		assert.Len(t, authors, 1)
		assert.Equal(t, testHFAuthor1, authors[0].Name)
	})
}

// ---------------------------------------------------------------------------
// Test: Sort order mapping
// ---------------------------------------------------------------------------

func TestHFSortOrderMapping(t *testing.T) {
	t.Parallel()

	t.Run("model_sort_relevance", func(t *testing.T) {
		t.Parallel()
		field, direction := mapHFModelSortOrder(SortRelevance)
		assert.Empty(t, field)
		assert.Empty(t, direction)
	})

	t.Run("model_sort_date_desc", func(t *testing.T) {
		t.Parallel()
		field, direction := mapHFModelSortOrder(SortDateDesc)
		assert.Equal(t, hfSortCreatedAt, field)
		assert.Equal(t, hfDirectionDesc, direction)
	})

	t.Run("model_sort_date_asc", func(t *testing.T) {
		t.Parallel()
		field, direction := mapHFModelSortOrder(SortDateAsc)
		assert.Equal(t, hfSortCreatedAt, field)
		assert.Equal(t, hfDirectionAsc, direction)
	})

	t.Run("model_sort_citations", func(t *testing.T) {
		t.Parallel()
		field, direction := mapHFModelSortOrder(SortCitations)
		assert.Equal(t, hfSortDownloads, field)
		assert.Equal(t, hfDirectionDesc, direction)
	})

	t.Run("dataset_sort_date_desc", func(t *testing.T) {
		t.Parallel()
		field, direction := mapHFDatasetSortOrder(SortDateDesc)
		assert.Equal(t, hfSortLastModified, field)
		assert.Equal(t, hfDirectionDesc, direction)
	})

	t.Run("dataset_sort_date_asc", func(t *testing.T) {
		t.Parallel()
		field, direction := mapHFDatasetSortOrder(SortDateAsc)
		assert.Equal(t, hfSortLastModified, field)
		assert.Equal(t, hfDirectionAsc, direction)
	})
}

// ---------------------------------------------------------------------------
// Test: Empty content type defaults to paper
// ---------------------------------------------------------------------------

func TestHuggingFaceSearchEmptyContentTypeDefaultsPaper(t *testing.T) {
	t.Parallel()

	var papersHit, modelsHit, datasetsHit atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case hfAPIPapersSearchPath:
			papersHit.Store(true)
			fmt.Fprint(w, buildHFTestPapersSearchJSON([]hfTestPaper{defaultHFTestPaper1()}))
		case hfAPIModelsPath:
			modelsHit.Store(true)
			fmt.Fprint(w, "[]")
		case hfAPIDatasetsPath:
			datasetsHit.Store(true)
			fmt.Fprint(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	plugin := newHFTestPlugin(t, server.URL)
	result, err := plugin.Search(context.Background(), SearchParams{
		Query:       "test",
		ContentType: "", // empty — should default to papers
		Limit:       10,
	}, nil)

	require.NoError(t, err)
	assert.Len(t, result.Results, 1)
	assert.True(t, papersHit.Load(), "papers API should be hit for empty content_type")
	assert.False(t, modelsHit.Load(), "models API should NOT be hit")
	assert.False(t, datasetsHit.Load(), "datasets API should NOT be hit")
}

// ---------------------------------------------------------------------------
// Test: resolveHFToken
// ---------------------------------------------------------------------------

func TestResolveHFToken(t *testing.T) {
	t.Parallel()

	t.Run("nil_creds_returns_server_default", func(t *testing.T) {
		t.Parallel()
		token := resolveHFToken(nil, testHFAPIKey)
		assert.Equal(t, testHFAPIKey, token)
	})

	t.Run("per_call_overrides_server", func(t *testing.T) {
		t.Parallel()
		creds := &CallCredentials{HFToken: testHFPerCallKey}
		token := resolveHFToken(creds, testHFAPIKey)
		assert.Equal(t, testHFPerCallKey, token)
	})

	t.Run("empty_per_call_falls_through_to_server", func(t *testing.T) {
		t.Parallel()
		creds := &CallCredentials{HFToken: ""}
		token := resolveHFToken(creds, testHFAPIKey)
		assert.Equal(t, testHFAPIKey, token)
	})

	t.Run("no_creds_no_server_returns_empty", func(t *testing.T) {
		t.Parallel()
		token := resolveHFToken(nil, "")
		assert.Empty(t, token)
	})
}

// ---------------------------------------------------------------------------
// Test: convertHFFormat direct
// ---------------------------------------------------------------------------

func TestConvertHFFormat(t *testing.T) {
	t.Parallel()

	t.Run("bibtex_unsupported", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			ID:        SourceHuggingFace + prefixedIDSeparator + hfSubTypePaper + testHFPaperID1,
			Title:     testHFTitle1,
			Authors:   []Author{{Name: testHFAuthor1}},
			Published: testHFDate1Short,
			URL:       hfPaperURLPrefix + testHFPaperID1,
		}
		err := convertHFFormat(pub, FormatBibTeX)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrFormatUnsupported))
	})

	t.Run("markdown_noop_when_already_present", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{
			FullText: &FullTextContent{
				Content:       testHFMarkdownContent,
				ContentFormat: FormatMarkdown,
				ContentLength: len(testHFMarkdownContent),
			},
		}
		err := convertHFFormat(pub, FormatMarkdown)
		require.NoError(t, err)
		assert.Equal(t, testHFMarkdownContent, pub.FullText.Content)
	})

	t.Run("markdown_error_when_no_full_text", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{}
		err := convertHFFormat(pub, FormatMarkdown)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrFormatUnsupported))
	})

	t.Run("xml_unsupported", func(t *testing.T) {
		t.Parallel()
		pub := &Publication{}
		err := convertHFFormat(pub, FormatXML)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrFormatUnsupported))
	})
}

// ---------------------------------------------------------------------------
// Test: Get dataset format and error paths
// ---------------------------------------------------------------------------

func TestHuggingFaceGetDatasetBibTeXUnsupported(t *testing.T) {
	t.Parallel()
	dataset := defaultHFTestDataset1()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, buildHFTestDatasetJSON(hfTestDataset{
			ID:          dataset.ID,
			Author:      dataset.Author,
			Likes:       dataset.Likes,
			Downloads:   dataset.Downloads,
			Tags:        dataset.Tags,
			CreatedAt:   dataset.CreatedAt,
			Description: dataset.Description,
		}))
	}))
	defer server.Close()

	plugin := newHFTestPlugin(t, server.URL)
	_, err := plugin.Get(context.Background(), hfSubTypeDataset+testHFDatasetID1, nil, FormatBibTeX, nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrFormatUnsupported))
}

func TestHuggingFaceGetDatasetServerError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	plugin := newHFTestPlugin(t, server.URL)
	_, err := plugin.Get(context.Background(), hfSubTypeDataset+testHFDatasetID1, nil, FormatNative, nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrGetFailed))
}

// ---------------------------------------------------------------------------
// Test: doRequestRaw error paths
// ---------------------------------------------------------------------------

func TestHuggingFaceDoRequestRawErrors(t *testing.T) {
	t.Parallel()

	t.Run("non_200_status", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		_, err := plugin.doRequestRaw(context.Background(), server.URL+"/test", "")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrHFHTTPRequest))
	})

	t.Run("context_canceled", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(testHFPluginTimeout)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		plugin := newHFTestPlugin(t, server.URL)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := plugin.doRequestRaw(ctx, server.URL+"/test", "")
		require.Error(t, err)
	})
}
