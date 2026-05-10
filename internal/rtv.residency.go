package internal

import "time"

// ---------------------------------------------------------------------------
// Residency types — Cycle 2 task #8
//
// SourcePlugin.Residency() returns a ResidencyTag for every registered
// plugin. The tag is the input to the EU-mode gate (Hook #2) and surfaces
// in rtv_list_sources for compliance review. Cycle-1 stub lived in
// pkg/retrievr/plugin/residency.go but couldn't be referenced by the
// internal SourcePlugin interface without an import cycle. This file is
// the new source of truth; pkg/retrievr/plugin re-exports via type alias.
// ---------------------------------------------------------------------------

// Region classifies a provider's data-residency posture.
type Region string

// Region values.
const (
	// RegionEU — data processed exclusively in EU/EEA member states.
	RegionEU Region = "EU"

	// RegionUKAdequacy — UK; covered by EU adequacy decision.
	RegionUKAdequacy Region = "UK-adequacy"

	// RegionUS — US-hosted provider; blocked under eu_strict.
	RegionUS Region = "US"

	// RegionGlobal — multi-region or unspecified hosting.
	RegionGlobal Region = "global"

	// RegionPublicResearch — public research infrastructure (e.g., ArXiv,
	// OpenAlex, Wikipedia). US-hosted but openly-accessible scientific
	// metadata; admissible under eu_strict only with explicit opt-in.
	RegionPublicResearch Region = "public-research-infrastructure"

	// RegionUnknown — residency not yet verified.
	RegionUnknown Region = "unknown"
)

// IsEU reports whether the region is treated as EU-resident under
// EUModeStrict (without the public-research opt-in).
func (r Region) IsEU() bool {
	return r == RegionEU || r == RegionUKAdequacy
}

// IsPublicResearch reports whether the region is the public-research-
// infrastructure tier (admissible under eu_strict + opt-in).
func (r Region) IsPublicResearch() bool {
	return r == RegionPublicResearch
}

// DPAStatus enumerates the contractual posture options.
type DPAStatus string

// DPAStatus values.
const (
	// DPASigned — operator has a signed Data Processing Agreement.
	DPASigned DPAStatus = "signed"

	// DPACoveredBySCC — covered by EU Standard Contractual Clauses.
	DPACoveredBySCC DPAStatus = "covered-by-scc"

	// DPANotApplicable — typically public-research-infrastructure tier.
	DPANotApplicable DPAStatus = "n/a"

	// DPAUnknown — residency claim not yet verified.
	DPAUnknown DPAStatus = "unknown"
)

// ResidencyTag declares a provider's data-residency posture for the
// EU-mode gate (Hook #1 of the six audit hooks, plan §3.7).
type ResidencyTag struct {
	// Region is the high-level jurisdiction classification.
	Region Region `json:"region"`

	// DPAStatus records the contractual posture.
	DPAStatus DPAStatus `json:"dpa_status,omitempty"`

	// SubprocessorURL is the provider's published sub-processor list URL,
	// used by adopters' compliance teams to monitor for changes.
	SubprocessorURL string `json:"subprocessor_url,omitempty"`

	// LastVerifiedAt is when the provider's residency claim was last
	// verified by a maintainer. CI warns when older than 90 days, hard-
	// fails at 180 days (cycle-3 enforcement).
	LastVerifiedAt time.Time `json:"last_verified_at"`
}
