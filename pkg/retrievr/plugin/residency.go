package plugin

import "time"

// ResidencyTag declares a provider's data-residency posture for EU-mode
// gating. Hook #1 of the six EU-mode audit hooks (see plan §3.7).
//
// Cycle-1 status: type defined, not yet returned by SourcePlugin. Cycle 2
// adds a Residency() method to the SourcePlugin interface and populates a
// tag for every registered provider; CI fails if LastVerifiedAt is older
// than 90 days.
type ResidencyTag struct {
	// Region is the high-level jurisdiction classification (EU, US, UK-adequacy,
	// public-research-infrastructure, etc.). String here rather than the
	// retrievr.Region enum to avoid an import cycle (retrievr → plugin → retrievr).
	// The retrievr package re-exports Region constants; callers compare strings.
	Region string `json:"region"`

	// DPAStatus records the contractual posture: "signed", "covered-by-scc",
	// "n/a" (e.g., public-research-infrastructure), or "unknown".
	DPAStatus string `json:"dpa_status,omitempty"`

	// SubprocessorURL is the provider's published sub-processor list URL,
	// used by adopters' compliance teams to monitor for changes.
	SubprocessorURL string `json:"subprocessor_url,omitempty"`

	// LastVerifiedAt is when the provider's residency claim was last
	// verified by a maintainer. CI warns when older than 90 days, hard-fails
	// at 180 days.
	LastVerifiedAt time.Time `json:"last_verified_at"`
}
