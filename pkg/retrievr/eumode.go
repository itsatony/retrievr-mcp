package retrievr

import "github.com/itsatony/retrievr-mcp/internal"

// EUMode controls how retrievr filters provider eligibility on data residency
// and GDPR grounds.
//
// Cycle-1 status: enum + helpers defined, not yet enforced. The full gate +
// six audit hooks land in cycle 2 (v1.6.0) — see project_plan/retrievr_v2.md
// §3.7 and §5.1.2.
type EUMode string

// EUMode states.
const (
	// EUModeOff applies no jurisdictional filter.
	EUModeOff EUMode = "off"

	// EUModePreferred tries EU-resident sources first; falls back to non-EU
	// with audit-logged warning when EU sources fail or yield zero.
	EUModePreferred EUMode = "eu_preferred"

	// EUModeStrict admits only EU-resident sources. Optional opt-in flag
	// (Client.WithEUMode includePublicResearch=true) admits public-research-
	// infrastructure providers (ArXiv, OpenAlex, CrossRef, Semantic Scholar,
	// PubMed, Wikipedia, Unpaywall).
	EUModeStrict EUMode = "eu_strict"
)

// IsValidEUMode returns true if the given string is a known EUMode.
func IsValidEUMode(m string) bool {
	switch EUMode(m) {
	case EUModeOff, EUModePreferred, EUModeStrict:
		return true
	}
	return false
}

// Region re-exports internal.Region — the canonical residency
// classification. Cycle 2 promoted Region from a public-only type to the
// SourcePlugin contract (Hook #1 of EU mode).
type Region = internal.Region

// Region values.
const (
	RegionEU             = internal.RegionEU
	RegionUKAdequacy     = internal.RegionUKAdequacy
	RegionUS             = internal.RegionUS
	RegionGlobal         = internal.RegionGlobal
	RegionPublicResearch = internal.RegionPublicResearch
	RegionUnknown        = internal.RegionUnknown
)

// DPAStatus re-exports the contractual-posture enum.
type DPAStatus = internal.DPAStatus

// DPAStatus values.
const (
	DPASigned        = internal.DPASigned
	DPACoveredBySCC  = internal.DPACoveredBySCC
	DPANotApplicable = internal.DPANotApplicable
	DPAUnknown       = internal.DPAUnknown
)

// ResidencyTag re-exports internal.ResidencyTag for direct cross-module
// use. Same shape as the SourcePlugin's Residency() return value.
type ResidencyTag = internal.ResidencyTag
