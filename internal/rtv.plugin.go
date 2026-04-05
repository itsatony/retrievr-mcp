package internal

import "context"

// SourcePlugin defines the contract for adding new publication/data sources.
// Implement this interface, register in the plugin registry, and the source
// is automatically available through rtv_search and rtv_get.
type SourcePlugin interface {
	// ID returns the unique source identifier (e.g., "arxiv", "pubmed").
	ID() string

	// Name returns a human-readable name (e.g., "ArXiv", "PubMed").
	Name() string

	// Description returns a short description for LLM context.
	Description() string

	// ContentTypes returns what this source provides (paper, model, dataset).
	ContentTypes() []ContentType

	// Capabilities reports what filtering, sorting, and features this source supports.
	Capabilities() SourceCapabilities

	// NativeFormat returns the default content format this source produces.
	NativeFormat() ContentFormat

	// AvailableFormats returns all formats this source can provide.
	AvailableFormats() []ContentFormat

	// Search executes a search query against this source.
	// Must respect ctx cancellation. Must handle its own rate limiting.
	// Credentials from the call override config-level defaults.
	Search(ctx context.Context, params SearchParams, creds *CallCredentials) (*SearchResult, error)

	// Get retrieves a single item by its source-specific ID.
	// The ID will already have the source prefix stripped (e.g., "2401.12345" not "arxiv:2401.12345").
	// format=FormatNative means "use NativeFormat()".
	Get(ctx context.Context, id string, include []IncludeField, format ContentFormat, creds *CallCredentials) (*Publication, error)

	// Initialize is called once at startup with the plugin's config.
	Initialize(ctx context.Context, cfg PluginConfig) error

	// Health returns current health and rate-limit status.
	Health(ctx context.Context) SourceHealth
}
