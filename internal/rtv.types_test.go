package internal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Constant value tests
// ---------------------------------------------------------------------------

func TestContentTypeConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    ContentType
		expected string
	}{
		{"paper", ContentTypePaper, "paper"},
		{"model", ContentTypeModel, "model"},
		{"dataset", ContentTypeDataset, "dataset"},
		{"any", ContentTypeAny, "any"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestContentFormatConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    ContentFormat
		expected string
	}{
		{"native", FormatNative, "native"},
		{"json", FormatJSON, "json"},
		{"xml", FormatXML, "xml"},
		{"markdown", FormatMarkdown, "markdown"},
		{"bibtex", FormatBibTeX, "bibtex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestIncludeFieldConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    IncludeField
		expected string
	}{
		{"abstract", IncludeAbstract, "abstract"},
		{"full_text", IncludeFullText, "full_text"},
		{"references", IncludeReferences, "references"},
		{"citations", IncludeCitations, "citations"},
		{"related", IncludeRelated, "related"},
		{"metadata", IncludeMetadata, "metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestSortOrderConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    SortOrder
		expected string
	}{
		{"relevance", SortRelevance, "relevance"},
		{"date_desc", SortDateDesc, "date_desc"},
		{"date_asc", SortDateAsc, "date_asc"},
		{"citations", SortCitations, "citations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.value))
		})
	}
}

func TestSourceIDConstants(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected string
	}{
		{"arxiv", SourceArXiv, "arxiv"},
		{"pubmed", SourcePubMed, "pubmed"},
		{"s2", SourceS2, "s2"},
		{"openalex", SourceOpenAlex, "openalex"},
		{"huggingface", SourceHuggingFace, "huggingface"},
		{"europmc", SourceEuropePMC, "europmc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.value)
		})
	}
}

func TestValidSourceIDs(t *testing.T) {
	expectedCount := 6
	assert.Len(t, ValidSourceIDs, expectedCount)

	expectedSources := []string{
		SourceArXiv, SourcePubMed, SourceS2,
		SourceOpenAlex, SourceHuggingFace, SourceEuropePMC,
	}
	for _, src := range expectedSources {
		assert.True(t, ValidSourceIDs[src], "expected %q in ValidSourceIDs", src)
	}

	assert.False(t, ValidSourceIDs["nonexistent"])
}

// ---------------------------------------------------------------------------
// JSON round-trip tests
// ---------------------------------------------------------------------------

func TestPublicationJSONRoundTrip(t *testing.T) {
	citations := 42
	pub := Publication{
		ID:            "arxiv:2401.12345",
		Source:        SourceArXiv,
		AlsoFoundIn:   []string{SourceS2, SourceOpenAlex},
		ContentType:   ContentTypePaper,
		Title:         "Attention Is All You Need (Again)",
		Authors:       []Author{{Name: "Jane Smith", Affiliation: "MIT"}},
		Published:     "2024-01-15",
		Updated:       "2024-02-01",
		Abstract:      "We present a new approach...",
		URL:           "https://arxiv.org/abs/2401.12345",
		PDFURL:        "https://arxiv.org/pdf/2401.12345",
		DOI:           "10.1234/example",
		ArXivID:       "2401.12345",
		Categories:    []string{"cs.CL", "cs.AI"},
		CitationCount: &citations,
		License:       "CC-BY-4.0",
	}

	data, err := json.Marshal(pub)
	require.NoError(t, err)

	var decoded Publication
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, pub.ID, decoded.ID)
	assert.Equal(t, pub.Source, decoded.Source)
	assert.Equal(t, pub.AlsoFoundIn, decoded.AlsoFoundIn)
	assert.Equal(t, pub.ContentType, decoded.ContentType)
	assert.Equal(t, pub.Title, decoded.Title)
	assert.Equal(t, pub.Authors, decoded.Authors)
	assert.Equal(t, pub.Published, decoded.Published)
	assert.Equal(t, pub.DOI, decoded.DOI)
	assert.Equal(t, pub.ArXivID, decoded.ArXivID)
	require.NotNil(t, decoded.CitationCount)
	assert.Equal(t, citations, *decoded.CitationCount)
}

func TestPublicationNilPointerFieldsJSON(t *testing.T) {
	pub := Publication{
		ID:          "arxiv:2401.00001",
		Source:      SourceArXiv,
		ContentType: ContentTypePaper,
		Title:       "Test",
		Authors:     []Author{{Name: "Test Author"}},
		Published:   "2024-01-01",
		URL:         "https://example.com",
	}

	data, err := json.Marshal(pub)
	require.NoError(t, err)

	// CitationCount is nil, should be omitted
	assert.NotContains(t, string(data), "citation_count")

	// FullText is nil, should be omitted
	assert.NotContains(t, string(data), "full_text")
}

func TestMergedSearchResultJSONRoundTrip(t *testing.T) {
	result := MergedSearchResult{
		TotalResults:   42,
		Results:        []Publication{{ID: "arxiv:001", Source: SourceArXiv, ContentType: ContentTypePaper, Title: "Test", Authors: []Author{{Name: "A"}}, Published: "2024-01-01", URL: "https://example.com"}},
		SourcesQueried: []string{SourceArXiv, SourceS2},
		SourcesFailed:  []string{},
		HasMore:        true,
	}

	data, err := json.Marshal(result)
	require.NoError(t, err)

	var decoded MergedSearchResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, result.TotalResults, decoded.TotalResults)
	assert.Len(t, decoded.Results, 1)
	assert.Equal(t, result.SourcesQueried, decoded.SourcesQueried)
	assert.Equal(t, result.HasMore, decoded.HasMore)
}

func TestSourceInfoJSONRoundTrip(t *testing.T) {
	info := SourceInfo{
		ID:                     SourceArXiv,
		Name:                   "ArXiv",
		Description:            "Open-access preprint server",
		Enabled:                true,
		ContentTypes:           []ContentType{ContentTypePaper},
		NativeFormat:           FormatXML,
		AvailableFormats:       []ContentFormat{FormatXML, FormatJSON, FormatBibTeX},
		SupportsFullText:       true,
		SupportsCitations:      false,
		SupportsDateFilter:     true,
		SupportsAuthorFilter:   true,
		SupportsCategoryFilter: true,
		RateLimit:              RateLimitInfo{RequestsPerSecond: 0.33, Remaining: 0.33},
		CategoriesHint:         "cs.AI, cs.CL, cs.LG",
		AcceptsCredentials:     false,
	}

	data, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded SourceInfo
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, info.ID, decoded.ID)
	assert.Equal(t, info.Name, decoded.Name)
	assert.Equal(t, info.RateLimit.RequestsPerSecond, decoded.RateLimit.RequestsPerSecond)
	assert.Equal(t, info.AvailableFormats, decoded.AvailableFormats)
	assert.Equal(t, info.AcceptsCredentials, decoded.AcceptsCredentials)
}

func TestAuthorJSONRoundTrip(t *testing.T) {
	author := Author{
		Name:        "Geoffrey Hinton",
		Affiliation: "University of Toronto",
		ORCID:       "0000-0001-2345-6789",
	}

	data, err := json.Marshal(author)
	require.NoError(t, err)

	var decoded Author
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, author, decoded)
}

func TestReferenceJSONRoundTrip(t *testing.T) {
	ref := Reference{
		ID:    "arxiv:1706.03762",
		Title: "Attention Is All You Need",
		Year:  2017,
	}

	data, err := json.Marshal(ref)
	require.NoError(t, err)

	var decoded Reference
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, ref, decoded)
}

func TestFullTextContentJSONRoundTrip(t *testing.T) {
	ft := FullTextContent{
		Content:       "<article>...</article>",
		ContentFormat: FormatXML,
		ContentLength: 21,
		Truncated:     false,
	}

	data, err := json.Marshal(ft)
	require.NoError(t, err)

	var decoded FullTextContent
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, ft, decoded)
}

func TestSearchResultJSONRoundTrip(t *testing.T) {
	sr := SearchResult{
		Total:   100,
		Results: []Publication{{ID: "s2:abc", Source: SourceS2, ContentType: ContentTypePaper, Title: "Test", Authors: []Author{{Name: "A"}}, Published: "2024-01-01", URL: "https://example.com"}},
		HasMore: true,
	}

	data, err := json.Marshal(sr)
	require.NoError(t, err)

	var decoded SearchResult
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, sr.Total, decoded.Total)
	assert.Len(t, decoded.Results, 1)
	assert.True(t, decoded.HasMore)
}

// ---------------------------------------------------------------------------
// CallCredentials tests
// ---------------------------------------------------------------------------

func TestCallCredentialsResolveForSource(t *testing.T) {
	const serverDefault = "server-key-123"
	const perCallKey = "per-call-key-456"

	tests := []struct {
		name          string
		creds         *CallCredentials
		sourceID      string
		serverDefault string
		expected      string
	}{
		{
			name:          "nil credentials returns server default",
			creds:         nil,
			sourceID:      SourcePubMed,
			serverDefault: serverDefault,
			expected:      serverDefault,
		},
		{
			name:          "empty per-call returns server default",
			creds:         &CallCredentials{},
			sourceID:      SourceS2,
			serverDefault: serverDefault,
			expected:      serverDefault,
		},
		{
			name:          "per-call pubmed key wins",
			creds:         &CallCredentials{PubMedAPIKey: perCallKey},
			sourceID:      SourcePubMed,
			serverDefault: serverDefault,
			expected:      perCallKey,
		},
		{
			name:          "per-call s2 key wins",
			creds:         &CallCredentials{S2APIKey: perCallKey},
			sourceID:      SourceS2,
			serverDefault: serverDefault,
			expected:      perCallKey,
		},
		{
			name:          "per-call openalex key wins",
			creds:         &CallCredentials{OpenAlexAPIKey: perCallKey},
			sourceID:      SourceOpenAlex,
			serverDefault: serverDefault,
			expected:      perCallKey,
		},
		{
			name:          "per-call hf token wins",
			creds:         &CallCredentials{HFToken: perCallKey},
			sourceID:      SourceHuggingFace,
			serverDefault: serverDefault,
			expected:      perCallKey,
		},
		{
			name:          "unknown source returns server default",
			creds:         &CallCredentials{PubMedAPIKey: perCallKey},
			sourceID:      SourceArXiv,
			serverDefault: serverDefault,
			expected:      serverDefault,
		},
		{
			name:          "empty server default and no per-call returns empty",
			creds:         &CallCredentials{},
			sourceID:      SourceS2,
			serverDefault: "",
			expected:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.creds.ResolveForSource(tt.sourceID, tt.serverDefault)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// SearchFilters pointer field tests
// ---------------------------------------------------------------------------

func TestSearchFiltersPointerFieldsJSON(t *testing.T) {
	t.Run("nil_pointers_omitted", func(t *testing.T) {
		f := SearchFilters{Title: "test"}
		data, err := json.Marshal(f)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "open_access")
		assert.NotContains(t, string(data), "min_citations")
	})

	t.Run("set_pointers_included", func(t *testing.T) {
		oa := true
		mc := 10
		f := SearchFilters{
			Title:        "test",
			OpenAccess:   &oa,
			MinCitations: &mc,
		}
		data, err := json.Marshal(f)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"open_access":true`)
		assert.Contains(t, string(data), `"min_citations":10`)
	})

	t.Run("false_pointer_included", func(t *testing.T) {
		oa := false
		f := SearchFilters{OpenAccess: &oa}
		data, err := json.Marshal(f)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"open_access":false`)
	})
}
