package retrievr

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

// Region classifies a provider's data-residency posture for EU-mode gating.
type Region string

// Region values.
const (
	// RegionEU — data processed exclusively in EU/EEA member states.
	RegionEU Region = "EU"

	// RegionUKAdequacy — UK, currently covered by EU adequacy decision.
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
