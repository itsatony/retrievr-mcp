package internal

import "time"

// Cycle 2 task #8 — residency tags for the 10 cycle-1 scholarly providers.
//
// Centralized in this file rather than scattered across every plugin so a
// quarterly residency audit reviews and bumps a single date variable. The
// EU-mode design (plan §3.7, ADR-018-pending) treats ANY non-zero
// LastVerifiedAt as in-policy at registration time; cycle 3 will surface
// CI warnings when the date is older than 90 days and hard-fail at 180.
//
// Region classifications follow plan §3.7's residency table for cycle-1
// providers:
//
//   DBLP                — EU (Schloss Dagstuhl, Germany)
//   Europe PMC          — UK-adequacy (EBI, Hinxton)
//   ArXiv               — public-research-infrastructure (Cornell, US)
//   OpenAlex            — public-research-infrastructure (OurResearch, US)
//   CrossRef            — public-research-infrastructure
//   Semantic Scholar    — public-research-infrastructure (AI2, US)
//   PubMed              — public-research-infrastructure (NLM, US)
//   HuggingFace         — US (US-resident; cycle 2 verifies EU-region option)
//   NASA ADS            — US (Harvard CfA)
//   bioRxiv             — US (CSHL)

// residencyVerifiedAt is the single source of truth for the
// LastVerifiedAt field on every cycle-1 provider's ResidencyTag.
// Bump this date when a maintainer re-verifies the residency table.
var residencyVerifiedAt = time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)

// Residency implementations — one method per plugin type.

// Residency reports ArXiv's data-residency posture.
func (*ArXivPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports Semantic Scholar's data-residency posture.
func (*S2Plugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports OpenAlex's data-residency posture.
func (*OpenAlexPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports PubMed's data-residency posture.
func (*PubMedPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports CrossRef's data-residency posture.
func (*CrossRefPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports DBLP's data-residency posture (EU — Schloss Dagstuhl, DE).
func (*DBLPPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports Europe PMC's data-residency posture (UK adequacy — EBI Hinxton).
func (*EuropePMCPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUKAdequacy,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports HuggingFace's data-residency posture (US, unverified).
func (*HuggingFacePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports NASA ADS's data-residency posture (US — Harvard CfA).
func (*ADSPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports bioRxiv's data-residency posture (US — CSHL).
func (*BioRxivPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports Stack Exchange's data-residency posture (US — Stack
// Exchange Inc., NYC). Content is licensed CC-BY-SA so admissible under
// eu_preferred with attribution; blocked under eu_strict. SCC coverage
// applies to the platform's processing of any opt-in account fields.
func (*StackExchangePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPACoveredBySCC,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports Hacker News's data-residency posture (US — Y
// Combinator). Public read-only Algolia mirror; no account binding for
// search, hence DPA unknown.
func (*HackerNewsPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports Zenodo's data-residency posture (EU — CERN, Geneva).
// Zenodo is operated by CERN under EU research-infrastructure governance;
// content is OA / CC licensed.
func (*ZenodoPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports CORE's data-residency posture (UK adequacy — Open
// University). UK adequacy decision keeps it admissible eu_preferred;
// blocked eu_strict unless IncludePublicResearch is set.
func (*COREPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUKAdequacy,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports OpenAIRE's data-residency posture (EU — Athena RIC,
// Greece). EU-funded research aggregator.
func (*OpenAIREPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports Wikidata's data-residency posture
// (public-research-infrastructure — Wikimedia Foundation).
func (*WikidataPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionPublicResearch,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports DataCite's data-residency posture
// (EU — DataCite e.V., Hannover DE).
func (*DataCitePlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionEU,
		DPAStatus:      DPANotApplicable,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports ORCID's data-residency posture (US — ORCID Inc.,
// Bethesda MD; non-profit). SCC applies for any account-linked
// processing of researcher profile data.
func (*ORCIDPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPACoveredBySCC,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports npm's data-residency posture (US — npm Inc., a
// subsidiary of GitHub / Microsoft). Blocked under eu_strict; the
// cycle 4 plan documents this as a known gap (no EU-resident
// package registry exists at scale).
func (*NPMPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPACoveredBySCC,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports PyPI's data-residency posture (US — Python
// Software Foundation, non-profit, US-based).
func (*PyPIPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports crates.io's data-residency posture (US — Rust
// Foundation, US-based non-profit).
func (*CratesPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPAUnknown,
		LastVerifiedAt: residencyVerifiedAt,
	}
}

// Residency reports pkg.go.dev's data-residency posture (US —
// Google, Mountain View). HTML search page; no user account binding
// for search, so DPA unknown.
func (*PkgGoDevPlugin) Residency() ResidencyTag {
	return ResidencyTag{
		Region:         RegionUS,
		DPAStatus:      DPACoveredBySCC,
		LastVerifiedAt: residencyVerifiedAt,
	}
}
