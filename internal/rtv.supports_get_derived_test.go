package internal

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// SourceCapabilities.SupportsGet is DERIVED, never hand-kept.
//
// Issue #1's defect is a capability the catalog did not carry. Answering it
// with a hand-maintained list of 61 booleans would reproduce the same defect
// one level up: the list and the code would drift the first time a plugin
// grows a real Get, and nothing would notice.
//
// So this test re-derives the truth from the SOURCE — it parses every
// internal/rtv.plugin.*.go file, classifies each plugin's Get method as a stub
// or a real implementation, and compares that to what the plugin DECLARES via
// Capabilities(). A disagreement in either direction fails.
//
// The classifier's rule is deliberately narrow: a Get is a STUB when its body
// is exactly one return statement handing back a get-unsupported refusal.
// Anything else — even a one-liner delegating to a helper — is real. Narrow is
// the safe direction here: an unrecognised shape is reported as real, so a
// plugin declaring SupportsGet:false with an unusual stub shape fails loudly
// rather than passing quietly.
// ---------------------------------------------------------------------------

const (
	// supportsGetPluginGlob is the file set the classifier walks. Every
	// SourcePlugin implementation in this tree lives in one of these.
	supportsGetPluginGlob = "rtv.plugin.*.go"

	// Recognised refusal constructors/sentinels. A Get whose whole body
	// returns one of these has no per-record retrieval API.
	supportsGetRefusalCtor     = "NewGetUnsupportedError"
	supportsGetRefusalSentinel = "ErrGetUnsupported"
	supportsGetLegacySentinel  = "ErrFormatUnsupported"

	// supportsGetMinPlugins is a NON-VACUITY FLOOR. A derived set that
	// resolves to zero (a changed glob, a renamed receiver, a parse error
	// swallowed) would make every assertion below pass without checking
	// anything. The floor is the registry's own size, asserted separately.
	supportsGetMinPlugins = 40
)

// getStubsByReceiver parses the plugin sources and returns receiver type name
// -> isStub for every Get method found. The second return is the count of Get
// methods seen, used as the non-vacuity measurement.
func getStubsByReceiver(t *testing.T) map[string]bool {
	t.Helper()

	files, err := filepath.Glob(supportsGetPluginGlob)
	require.NoError(t, err, "globbing plugin sources")
	require.NotEmpty(t, files, "the plugin glob matched no files — the classifier would be vacuous")

	stubs := make(map[string]bool, len(files))
	fset := token.NewFileSet()

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, perr := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, perr, "parsing %s", file)

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Get" || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if recv == "" {
				continue
			}
			_, dup := stubs[recv]
			require.False(t, dup, "two Get methods on receiver %s — the map would silently overwrite one", recv)
			stubs[recv] = isGetStubBody(fn.Body)
		}
	}
	return stubs
}

// receiverTypeName resolves "*LinkupPlugin" / "LinkupPlugin" to "LinkupPlugin".
func receiverTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// isGetStubBody reports whether a Get body is exactly one return statement
// producing a get-unsupported refusal.
func isGetStubBody(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 2 {
		return false
	}
	// Collect every identifier mentioned in the error expression and look
	// for a refusal marker. Matching identifiers rather than source bytes
	// means a reformat, a renamed detail string or a different wrapping
	// helper cannot change the verdict.
	found := false
	ast.Inspect(ret.Results[1], func(n ast.Node) bool {
		ident, isIdent := n.(*ast.Ident)
		if !isIdent {
			return true
		}
		switch ident.Name {
		case supportsGetRefusalCtor, supportsGetRefusalSentinel, supportsGetLegacySentinel:
			found = true
		}
		return true
	})
	return found
}

func TestSupportsGetMatchesEveryPluginsGetImplementation(t *testing.T) {
	t.Parallel()

	stubs := getStubsByReceiver(t)
	factories := PluginFactories()

	require.GreaterOrEqual(t, len(stubs), supportsGetMinPlugins,
		"the AST classifier found only %d Get methods — below the non-vacuity floor", len(stubs))

	var declaredTrue, declaredFalse int
	var unclassified []string

	for sourceID, factory := range factories {
		plugin := factory()
		recv := reflect.TypeOf(plugin).Elem().Name()

		isStub, seen := stubs[recv]
		if !seen {
			unclassified = append(unclassified, sourceID+" ("+recv+")")
			continue
		}

		declared := plugin.Capabilities().SupportsGet
		if declared {
			declaredTrue++
		} else {
			declaredFalse++
		}

		assert.Equal(t, !isStub, declared,
			"source %q (%s): Capabilities().SupportsGet=%v but its Get implementation is stub=%v — "+
				"the catalog must not claim a retrieval the plugin refuses, nor hide one it performs",
			sourceID, recv, declared, isStub)
	}

	assert.Empty(t, unclassified,
		"every registered plugin must have a Get method the classifier can see; unclassified: %v", unclassified)

	// The split must be non-trivial in BOTH directions. If it collapsed to
	// all-true or all-false the equality assertion above would still pass
	// while measuring nothing about the population the issue is about.
	assert.Positive(t, declaredTrue, "no plugin declares SupportsGet=true — the derivation is degenerate")
	assert.Positive(t, declaredFalse, "no plugin declares SupportsGet=false — the derivation is degenerate")
	assert.Equal(t, len(factories), declaredTrue+declaredFalse,
		"every registered plugin must be counted exactly once")

	t.Logf("derived SupportsGet over %d registered plugins: %d true, %d false",
		len(factories), declaredTrue, declaredFalse)

	// The rtv_get tool description states the split to the model. A number
	// typed by hand there is the same defect this test exists to prevent, so
	// derive it: adding a real Get to a 62nd plugin must fail here until the
	// sentence the model reads is corrected too.
	assert.Contains(t, ToolDescGet,
		fmt.Sprintf("Only %d of the %d plugins have a per-record retrieval API", declaredTrue, len(factories)),
		"the rtv_get tool description must state the DERIVED supports_get split")
}

// TestListSourcesPublishesSupportsGet pins the PROJECTION. Declaring the field
// on SourceCapabilities is not publishing it: rtv_list_sources builds a
// separate SourceInfo struct field by field, and a field nobody copies there
// is invisible to every caller.
func TestListSourcesPublishesSupportsGet(t *testing.T) {
	t.Parallel()

	router := newSupportsGetTestRouter(t)
	infos := router.ListSources(t.Context())
	require.NotEmpty(t, infos)

	byID := make(map[string]SourceInfo, len(infos))
	for _, info := range infos {
		byID[info.ID] = info
	}

	require.Contains(t, byID, SourceArXiv)
	require.Contains(t, byID, SourceLinkup)
	assert.True(t, byID[SourceArXiv].SupportsGet, "arxiv has a real Get and must advertise it")
	assert.False(t, byID[SourceLinkup].SupportsGet, "linkup has no per-result Get API and must say so")
}

// TestRouterGetRefusesBeforeDispatchWhenUnsupported is the behavioural half:
// the refusal must happen in the router, with the plugin never called.
func TestRouterGetRefusesBeforeDispatchWhenUnsupported(t *testing.T) {
	t.Parallel()

	spy := &supportsGetSpyPlugin{}
	router := newTestRouterWithPlugins(t, map[string]SourcePlugin{spy.ID(): spy})

	pub, err := router.Get(t.Context(), spy.ID()+":deadbeef", nil, FormatNative, nil)

	require.Error(t, err)
	assert.Nil(t, pub)
	assert.Zero(t, spy.getCalls, "the plugin must not be dispatched at all when SupportsGet is false")
	assert.ErrorIs(t, err, ErrGetUnsupported, "the refusal must carry the precise sentinel")
	assert.ErrorIs(t, err, ErrFormatUnsupported, "and must stay compatible with the historical one")
	assert.Contains(t, err.Error(), "url", "the refusal must name the remedy")
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const supportsGetSpySourceID = SourceLinkup

// supportsGetSpyPlugin declares SupportsGet=false and counts dispatches. The
// count is the assertion: a router that refused only AFTER calling the plugin
// would return the same error and pass a test that read the error alone.
type supportsGetSpyPlugin struct {
	getCalls int
}

func (p *supportsGetSpyPlugin) ID() string          { return supportsGetSpySourceID }
func (p *supportsGetSpyPlugin) Name() string        { return "spy" }
func (p *supportsGetSpyPlugin) Description() string { return "spy plugin for the SupportsGet gate" }
func (p *supportsGetSpyPlugin) ContentTypes() []ContentType {
	return []ContentType{ContentTypeAny}
}

func (p *supportsGetSpyPlugin) Capabilities() SourceCapabilities {
	return SourceCapabilities{
		SupportsGet:        false,
		MaxResultsPerQuery: 10,
		NativeFormat:       FormatJSON,
		AvailableFormats:   []ContentFormat{FormatJSON},
	}
}

func (p *supportsGetSpyPlugin) NativeFormat() ContentFormat { return FormatJSON }
func (p *supportsGetSpyPlugin) AvailableFormats() []ContentFormat {
	return []ContentFormat{FormatJSON}
}

func (p *supportsGetSpyPlugin) Search(context.Context, SearchParams) (*SearchResult, error) {
	return &SearchResult{}, nil
}

func (p *supportsGetSpyPlugin) Get(context.Context, string, []IncludeField, ContentFormat) (*Publication, error) {
	p.getCalls++
	return nil, NewGetUnsupportedError("spy has no per-result Get API")
}

func (p *supportsGetSpyPlugin) Initialize(context.Context, PluginConfig) error { return nil }

func (p *supportsGetSpyPlugin) Health(context.Context) SourceHealth {
	return SourceHealth{Enabled: true, Healthy: true, RateLimit: testRateLimitRPS}
}

func (p *supportsGetSpyPlugin) Residency() ResidencyTag { return ResidencyTag{} }

// newTestRouterWithPlugins builds a Router over an explicit plugin map.
func newTestRouterWithPlugins(t *testing.T, plugins map[string]SourcePlugin) *Router {
	t.Helper()
	cfg := testRouterConfig()
	for id := range plugins {
		cfg.DefaultSources = append(cfg.DefaultSources, id)
	}
	return NewRouter(cfg, plugins, nil, nil, testRateLimits(plugins), &CredentialResolver{}, nil, discardLogger())
}

// newSupportsGetTestRouter registers the REAL arxiv and linkup plugins so the
// projection assertion reads production capability values, not a fixture's.
func newSupportsGetTestRouter(t *testing.T) *Router {
	t.Helper()
	plugins := map[string]SourcePlugin{}
	for _, id := range []string{SourceArXiv, SourceLinkup} {
		factory, ok := PluginFactories()[id]
		require.True(t, ok, "source %q must be in the factory map", id)
		plugin := factory()
		require.NoError(t, plugin.Initialize(t.Context(), PluginConfig{Enabled: true}))
		plugins[id] = plugin
	}
	return newTestRouterWithPlugins(t, plugins)
}
