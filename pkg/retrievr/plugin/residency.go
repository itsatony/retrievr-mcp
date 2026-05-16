package plugin

import "github.com/itsatony/retrievr-mcp/v2/internal"

// ResidencyTag re-exports internal.ResidencyTag — the canonical residency
// posture record. Cycle 2 (v1.6.0) added a Residency() method to
// SourcePlugin and made this the source of truth for the EU-mode gate
// (Hook #1, plan §3.7).
type ResidencyTag = internal.ResidencyTag

// Region re-exports the region enum + values for cross-module callers.
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
