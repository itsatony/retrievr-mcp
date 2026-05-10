// Package providers groups the per-source SourcePlugin implementations.
//
// Cycle-1 status: empty placeholder. The 10 existing scholarly providers
// migrate from internal/rtv.plugin.<source>.go to providers/<source>/plugin.go
// in cycle 1 task #2 (when SourcePlugin loses its creds parameter and gains
// Residency()). Wave-1 providers (exa, brave, linkup, firecrawl, github,
// unpaywall, wikipedia) land in cycle 2 (v1.6.0).
//
// Each provider is its own subpackage so adopters who only need a subset
// (e.g., just academic sources) can build a leaner binary. The Registry in
// pkg/retrievr/plugin governs which subset is active at runtime.
package providers
