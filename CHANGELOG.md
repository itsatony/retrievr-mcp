# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [2.5.0] - 2026-05-11

Minor release. Fourth cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr-v3.md`). Third multimodal content class lands:
**image search** with two new EU/public-research providers plus an
extension of the existing Brave plugin for web image SERP.
**License is first-class** — openly-licensed reuse requires it.

### Added

- **`KindImage` ResultKind** + **`ImageData` per-kind block**:
  media_url, thumbnail_url, media_mime, width, height, license,
  license_url, artist, source_page. Converter routes `ContentTypeImage`
  → `Result.Image`. License + LicenseURL + Artist are first-class so
  downstream consumers can refuse to use unlicensed images.
- **`WikimediaPlugin`** (`SourceWikimedia = "wikimedia"`):
  - MediaWiki API single-call shape via `generator=search` +
    `prop=imageinfo` (no second roundtrip per result).
  - License + LicenseUrl + Artist extracted from `extmetadata`;
    Artist's surrounding HTML tags stripped via shared `stripHTMLTags`.
  - Composite ID `wikimedia:File:Mona_Lisa.jpg` matches the dedup key
    (`MetaKeyWikimediaFile`) so cross-provider merging is automatic.
  - Stable result order via `sortByIndex` (MediaWiki returns pages as
    a map → insertion order non-deterministic).
  - Residency: `public-research-infrastructure` — admissible under
    `eu_strict` with the `include_public_research` opt-in.
- **`EuropeanaPlugin`** (`SourceEuropeana = "europeana"`):
  - `/record/v2/search.json` with `qf=TYPE:IMAGE` + `media=true`.
  - Europeana's wskey passed via `X-Retrievr-Cred-europeana` per-call
    or `RETRIEVR_EUROPEANA_API_KEY` env.
  - MIME inferred from URL extension (Europeana doesn't surface it).
  - License taken from `Rights` (Europeana's rights are URLs).
  - Residency: `EU` + `DPASigned` (The Hague, NL). Admissible under
    `eu_strict`.
- **Brave image-search extension**:
  - New `/res/v1/images/search` dispatch when
    `params.ContentType == ContentTypeImage`. Web/news path unchanged.
  - `ContentTypes()` now reports `[Any, Image]`. `Capabilities().Kinds`
    adds `KindImage`.
  - **License intentionally NOT fabricated** — Brave's image SERP
    doesn't carry license info, so `Publication.License` stays empty
    as the explicit "unverified" signal.
  - Brave is NOT a new SourceID; existing API key continues to cover
    image search.
- **SourceMetadata image keys**: `smetaWidth`, `smetaHeight`,
  `smetaLicenseURL`, `smetaArtist`, `smetaSourcePage`. Converter reads
  them for `Result.Image`.
- **Config blocks** for `wikimedia` and `europeana` in
  `configs/retrievr-mcp.yaml` (disabled by default; Wikimedia carries
  a `user_agent` placeholder).

### Changed

- **`SourceCount`** bumped 23 → 25 (Brave gets new behavior without a
  new SourceID).
- **Plugin registry** registers Wikimedia + Europeana factories.
- **Brave's `Capabilities().Kinds`** gains `KindImage`.
- Test fixtures `TestLoadConfigAllSources` +
  `TestE2EConfigToTypesToErrors` + `TestInitializePlugins/all_sources_enabled`
  + `testRegistryExpectedFactoryCount` updated for 25 sources.

### Tests

- **27 new tests** total:
  - 10 in `rtv.plugin.wikimedia_test.go`: identity, capabilities,
    residency (public-research tag for eu_strict opt-in), happy path
    with license + artist + HTML tag stripping, filters out entries
    without MediaURL, stable order by search-rank `Index` (map-
    iteration determinism), 429 mapping, API-error propagation,
    FormatUnsupported on Get, `normalizeWikimediaFile` helper.
  - 10 in `rtv.plugin.europeana_test.go`: identity, capabilities,
    residency (EU + DPASigned), happy path with rights/creator/year,
    per-call credential, missing credential, 401 mapping,
    filters items without MediaURL, FormatUnsupported,
    `sanitizeEuropeanaID`, `inferMimeFromURL` (8 cases),
    `firstSliceValue`.
  - 5 in `rtv.plugin.brave_image_test.go`: ContentTypes surfaces
    Image, Kinds include KindImage, dispatch to `/images/search` on
    `ContentType=Image`, web path stays on `/web/search` for non-image
    types, filters results without MediaURL, License stays empty,
    `braveImageFormatToMime` helper.
  - 2 in `rtv.image_xprovider_dedup_test.go`: Router-level
    cross-provider dedup on `wikimedia_file` (Wikimedia +
    Europeana → 1 Result) and MediaURL fallback (Brave + Wikimedia
    → 1 Result when wikimedia_file is absent).

### EU-strict notes

- **Pure-EU coverage**: Europeana alone (DPASigned) — recommended for
  strict deployments.
- **EU-strict + public-research opt-in**: Europeana + Wikimedia —
  significantly broader catalog.
- **Brave images** stays blocked under `eu_strict` (US residency
  unchanged). Web-image SERP is opt-in for `off` / `eu_preferred`.

### Migration notes

- **No action required** for existing deployments. New sources default
  to `enabled: false`. Brave operators get image-search dispatch for
  free once they pass `content_type: image` in `rtv_search`.
- **Wikimedia operators**: override the placeholder User-Agent before
  enabling — same etiquette as Wikipedia/Nominatim.

## [2.4.0] - 2026-05-11

Minor release. Third cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr_v3.md`). Second multimodal content class lands:
**place search** with three EU-resident providers. First v3 cycle with
**full eu_strict coverage at zero baseline cost** — all three plugins
are admissible under the EU-mode gate.

### Added

- **`KindPlace` ResultKind** + **`PlaceData` per-kind block** on `Result`
  (osm_id, osm_type, lat, lon, address, country, country_code, city,
  state, postcode, street, house_number, categories, place_type,
  importance). Converter routes `ContentTypePlace` → `Result.Place`.
- **`PhotonPlugin`** (`SourcePhoton = "photon"`):
  - GeoJSON parsing of Komoot's `/api` endpoint (FeatureCollection →
    Publication with Lat/Lon/Address pointer fields).
  - Configurable `BaseURL` for self-hosting (planet DB ~95 GB, 64 GB RAM).
  - Composite osm_id `<osm_type>:<osm_id>` matches Nominatim's form
    so cross-provider dedup hits without extra glue.
  - Coarse `place_type` derivation from `osm_key`/`osm_value`.
  - Fallback Title composition (street + housenumber + city + country)
    when `properties.name` is empty.
  - Residency: `EU` + `DPANotApplicable` (Komoot, Berlin DE). **Admissible
    under `eu_strict`.** No auth required.
- **`TomTomPlugin`** (`SourceTomTom = "tomtom"`):
  - `GET /search/2/search/{q}.json` with URL-encoded query in the path
    and key as a query parameter.
  - 2,500 requests/day free tier; ~$0.75/1k paid beyond.
  - POI handling: `result.poi.name` wins over `address.freeformAddress`
    when present; categories merged into SourceMetadata.
  - Score-to-importance normalization via `1 - 1/(1+score)` so callers
    have a comparable [0,1] signal across all three place plugins.
  - `HasMore` derived from `summary.totalResults > numResults+offset`.
  - Residency: `EU` + `DPASigned` (TomTom, Amsterdam NL). **Admissible
    under `eu_strict`.**
- **`NominatimPlugin`** (`SourceNominatim = "nominatim"`):
  - OSM reference geocoder with **strict policy enforcement**:
    - 1 RPS HARD cap on the public endpoint, enforced in Initialize
      regardless of `cfg.RateLimit`. Self-hosters override via BaseURL.
    - User-Agent header always sent (OSMF policy); default placeholder
      with retrievr identification, operators MUST override.
  - 403 (UA non-compliance ban) mapped to `ErrCredentialInvalid` with
    explicit message pointing at policy compliance.
  - City fallback chain `city → town → village` covers OSMs varying
    admin keys.
  - Importance signal passed through to PlaceData.Importance directly
    (Nominatim's native 0-1 score).
  - License field populated from response (`Licence`).
  - Residency: `UKAdequacy` + `DPANotApplicable` (OSMF, UK).
    **Admissible under `eu_strict`** (Region.IsEU() returns true).
- **SourceMetadata place keys**: `smetaOSMType`, `smetaCountry`,
  `smetaCountryCode`, `smetaCity`, `smetaState`, `smetaPostcode`,
  `smetaStreet`, `smetaHouseNumber`, `smetaCategories`, `smetaPlaceType`,
  `smetaImportance`. Converter reads them for `Result.Place`.
- **`metaFloat`** helper in converter — float64-aware sibling of `metaInt`,
  used to surface `importance` on PlaceData.
- **Config blocks** for all three providers in `configs/retrievr-mcp.yaml`
  (disabled by default; Nominatim block includes a `user_agent`
  placeholder with an explicit "REPLACE-WITH-YOUR-CONTACT" reminder per
  OSMF policy).

### Changed

- **`SourceCount`** bumped 20 → 23 (added `photon`, `tomtom`,
  `nominatim`).
- **Plugin registry** registers all three new factories.
- **Test fixtures** for `TestLoadConfigAllSources`,
  `TestE2EConfigToTypesToErrors`, and
  `TestInitializePlugins/all_sources_enabled` updated to include the
  three new sources. `testRegistryExpectedFactoryCount` bumped to 23.

### Tests

- **22 new tests** total:
  - 7 in `rtv.plugin.photon_test.go`: identity, capabilities, residency
    (EU admissibility under eu_strict), happy-path wire shape, composite
    name fallback, 429 → ErrRateLimitExceeded, FormatUnsupported on Get,
    limit clamping, live smoke gated on `RETRIEVR_LIVE_PHOTON`.
  - 7 in `rtv.plugin.tomtom_test.go`: identity, capabilities, residency
    (EU + DPASigned), happy path with score→importance derivation, per-
    call credential override, 401/429 error mapping, FormatUnsupported,
    HasMore from totalResults/numResults math.
  - 6 in `rtv.plugin.nominatim_test.go`: identity, capabilities,
    residency (UK-adequacy + IsEU), **public-endpoint forces 1 RPS
    regardless of cfg** (policy enforcement), self-hosted allows higher
    rate, happy path (UA header propagation, address parsing, importance,
    license), default UA placeholder identifies retrievr,
    429/403 → typed errors, FormatUnsupported, city/town/village
    fallback.
  - 2 in `rtv.place_xprovider_dedup_test.go`: end-to-end Router-level
    dedup on `osm_id` composite (Photon + Nominatim merge); secondary
    dedup on rounded (lat, lon) when osm_id is absent (TomTom + Photon).

### EU-strict coverage milestone

`eu_strict` deployments now have **zero-baseline place coverage**:
- Photon and Nominatim require no credentials and run free.
- TomTom is paid-tier optional for stronger POI coverage.
- All three are admissible under the EU-mode gate without
  `include_public_research` opt-in (TomTom + Photon are pure EU;
  Nominatim is UK-adequacy which `Region.IsEU()` accepts).

### Migration notes

- **No action required** for existing deployments. New sources default to
  `enabled: false`.
- **Nominatim operators**: MUST override `extra.user_agent` with their own
  app + contact email before enabling. The placeholder UA is intentionally
  flagged so deployments don't ship it to the OSMF public endpoint.

## [2.3.0] - 2026-05-11

Minor release. Second cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr_v3.md`). First multimodal content class lands:
**video search** via the official YouTube Data API v3 (primary) and the
Scrapingdog YouTube SERP API (paid fallback). Cross-provider dedup on
`youtube_id` flows end-to-end through Router.Search.

### Added

- **`KindVideo` ResultKind** + **`VideoData` per-kind block** on `Result`
  (channel_name, channel_id, video_id, thumbnail_url, duration_seconds,
  view_count, like_count, published_at, live_broadcast). Cycle 1's
  `ContentTypeVideo` now flows through the v2 wire as `Result.Kind="video"`
  with `Result.Video.*` populated. `kindForSource` maps ContentType →
  ResultKind for all four v3 multimodal types (video/place/image/post),
  preparing the converter for cycles 3–5.
- **`YouTubePlugin`** (`SourceYouTube = "youtube"`):
  - Search via `GET /youtube/v3/search` (snippet only — lightweight),
    Get via `GET /youtube/v3/videos` (adds duration + view/like counts).
  - API key via `X-Retrievr-Cred-youtube` per-call header or
    `RETRIEVR_YOUTUBE_API_KEY` env / YAML.
  - ISO 8601 duration parser (`PT1H2M3S` → 3723 seconds).
  - Thumbnail picker (maxres → standard → high → medium → default).
  - Date filter mapped to `publishedAfter` / `publishedBefore`.
  - Sort: `relevance` → `relevance`; `date_*` → `date`.
  - `quotaExceeded` / `dailyLimitExceeded` / `rateLimitExceeded` API
    error reasons map to `ErrRateLimitExceeded` **without flipping
    Health.Healthy=false** — quota exhaustion is throttling, not failure.
  - `keyInvalid` → `ErrCredentialInvalid`. 401/403 → `ErrCredentialInvalid`.
  - Residency: `US` + `DPACoveredBySCC`. Blocked under `eu_strict`.
- **`ScrapingdogYouTubePlugin`** (`SourceScrapingdogYouTube = "scrapingdog_youtube"`):
  - Paid SERP fallback. `GET https://api.scrapingdog.com/youtube/search/`.
  - Wall-clock duration parser (`H:MM:SS` / `M:SS`).
  - View-count parser (`1.2K`, `1.2M`, `1,234,567`, `1.5B`).
  - YouTube watch URL → videoId extractor (handles `youtube.com/watch?v=`,
    `youtu.be/`, plus query-param trailers like `&list=`).
  - Skips non-video results (channel pages, playlists) silently.
  - 402 Payment Required also maps to `ErrCredentialInvalid` (Scrapingdog's
    "out of credits" path).
  - Residency: `US`. Blocked under `eu_strict`.
  - No `Get` — Scrapingdog's per-video endpoint is a separate product;
    callers should resolve detail via the `youtube` plugin.
- **SourceMetadata video keys**: `smetaChannelName`, `smetaChannelID`,
  `smetaViewCount`, `smetaLikeCount`, `smetaLiveBroadcast`. Converter
  reads them when populating `Result.Video`.
- **Config blocks** for both providers in `configs/retrievr-mcp.yaml`
  (disabled by default; flip `enabled: true` + set `api_key`).

### Changed

- **`SourceCount`** bumped 18 → 20 (added `youtube`, `scrapingdog_youtube`).
- **Plugin registry** registers both new factories.
- Test fixtures `TestLoadConfigAllSources` + `TestE2EConfigToTypesToErrors`
  + `TestInitializePlugins/all_sources_enabled` updated to include the
  two new sources. `testRegistryExpectedFactoryCount` bumped to 20.

### Tests

- **27 new tests** total:
  - 17 in `rtv.plugin.youtube_test.go`: identity, capabilities, content
    types, residency, happy-path search wire shape (video-only filter,
    snippet → Publication mapping), per-call credential override,
    missing credential, quota-exceeded (with Health.Healthy stays true
    invariant), keyInvalid, 401, full Get with duration + view + like
    counts, not-found, empty ID, limit clamping to cap, date filter
    formatting, sort=date mapping, helper unit tests
    (`parseISO8601DurationSeconds`, `pickBestThumbnail`), live smoke
    (gated on `YOUTUBE_API_KEY`).
  - 9 in `rtv.plugin.scrapingdog_youtube_test.go`: identity, capabilities,
    residency, happy path (channel + thumbnail + duration + view count
    parsing), skips non-video URLs, per-call credential, missing
    credential, 402/429 error mapping, no-Get, limit clamps, helpers
    (`extractYouTubeVideoID` with 8 cases incl. youtu.be short URL and
    `&list=` trailer; `parseClockDurationSeconds`; `parseViewCount`
    with NaN/Inf hardening).
  - 1 in `rtv.video_xprovider_dedup_test.go`: end-to-end Router.Search
    proves a video appearing on both `youtube` and `scrapingdog_youtube`
    merges to a single Result via `MetaKeyYouTubeID`, with the other
    provider recorded in `AlsoFoundIn`.

### Migration notes

- **No action required** for existing deployments. New sources default to
  `enabled: false`. Set `auth.mode: per_request` for multi-tenant quota
  isolation (no server-side YouTube key).
- **eu_strict deployments** see no video coverage — both providers are
  US-resident. Plan acknowledges this gap; an EU YouTube replacement does
  not exist.

## [2.2.0] - 2026-05-11

Minor release. First cycle of the v3 "Multimodal Retrieval" initiative
(`project_plan/retrievr_v3.md`). Pure type-model extension — no new plugins,
no behavioral change for the existing 18 sources. Lays the ContentType +
`Publication` + dedup foundation for cycles 2–5 (video / place / image /
post providers).

### Added

- **Four new `ContentType` values**: `video`, `place`, `image`, `post`.
  ContentTypeAny continues to match the full set including the new values.
- **`IsValidContentType(string) bool`** helper. Empty string is intentionally
  invalid; ContentTypeAny is valid.
- **`Publication` multimodal fields** (all optional, ContentType-gated):
  `ThumbnailURL`, `DurationSeconds`, `Lat`, `Lon`, `Address`, `MediaURL`,
  `MediaMime`, `EngagementScore`, `Language`. Paper / model / dataset
  results leave these nil — existing snapshots stay byte-stable.
- **SourceMetadata key constants** for v3 dedup: `MetaKeyYouTubeID` (video),
  `MetaKeyOSMID` (place), `MetaKeyWikimediaFile` (image), `MetaKeyAtprotoURI`
  (post). Plugins populate these so Router.dedup() can merge cross-source
  duplicates within a content class.
- **`rtv_search` `content_type` enum** extended with the four new values.
  Default remains `paper`; existing callers see no behavior change.
- **`ErrBibTeXUnsupported`** sentinel + `bibtexSupported(ContentType) bool`.
  GenerateBibTeX returns this typed error for video/place/image/post rather
  than emitting a misleading `@misc` entry. paper/model/dataset/"" continue
  to work unchanged.

### Changed

- **`Router.dedup()` refactored** to dispatch by `ContentType` using a
  single composite `(family, value)` index. Dedup-key family per class:
    - paper / model / dataset / "" / any → DOI, then ArXiv ID (unchanged)
    - video → `SourceMetadata["youtube_id"]`
    - place → `SourceMetadata["osm_id"]`, then `(lat, lon)` rounded to 5 dp
    - image → `SourceMetadata["wikimedia_file"]`, then `MediaURL`
    - post  → `SourceMetadata["atproto_uri"]`, then `URL`
  Cross-class dedup is impossible by construction — a video and a paper
  with identical string keys never collide.

### Tests

- **15 new tests** in `rtv.dedup_multimodal_test.go` + `rtv.bibtex_multimodal_test.go`
  + `rtv.types_test.go` + `rtv.tools_test.go`:
    - video dedup by `youtube_id` (merge, differ, empty-key non-collision)
    - place dedup by `osm_id` and by coord rounding (5 dp), divergent coords kept,
      missing coords kept
    - image dedup by `wikimedia_file` and by `MediaURL` fallback
    - post dedup by `atproto_uri` and by `URL` fallback
    - cross-class non-merge (paper + video with overlapping metadata; place + video)
    - paper-by-DOI regression guard (v2 behavior unchanged)
    - GenerateBibTeX returns `ErrBibTeXUnsupported` for v3 types
    - GenerateBibTeX still works for paper/model/dataset/""
    - `content_type` enum schema contains all 8 values
    - `IsValidContentType` accepts all 8 values, rejects empty + unknown

### Migration notes

- **No action required.** Pre-1.0 Go interface stability promises do not
  apply; nothing in v2.1.0 callers breaks. The MCP tool surface is
  additive-only — new enum values accepted, old defaults preserved.

## [2.1.0] - 2026-05-10

Minor release. Adds a multi-tenant authentication mode where the server
process NEVER carries source credentials — each tenant supplies their
own keys per-request via HTTP headers (or per-call via ctx). Existing
`hybrid` deployments are unaffected; the new `per_request` mode is
strictly opt-in.

### Added

- **`auth.mode`** config key with three modes:
  - `hybrid` (default; backward-compatible) — YAML `sources.<id>.api_key`
    + `RETRIEVR_<SOURCE>_API_KEY` env overrides act as fallbacks when
    no per-call credential is attached.
  - `per_request` — multi-tenant gateway. YAML api_keys are CLEARED at
    `LoadConfig`, env-var overrides are skipped, and any required-auth
    source called without ctx-attached credentials returns
    `ErrCredentialRequired`. Each request must carry its own keys via
    `X-Retrievr-Cred-<source>` headers (HTTP/MCP transport) or via
    `WithCredentials` (library callers).
  - `server_side` — YAML api_keys honored, ctx credentials IGNORED.
- **`X-Retrievr-Cred-<source>` HTTP header convention** for per-tenant
  credentials. The new `PerRequestCredsContextFunc` HTTP context-func
  extracts these headers into a ctx-attached map. Wired by default on
  the StreamableHTTPServer.
- **`ClearServerCredentials`** — exported helper that wipes any
  `sources.<id>.api_key` values that may have leaked into the YAML.
  Idempotent; returns cleared source IDs for security-warning logs.
- 8 new unit tests covering the auth-mode resolver, server-side
  credential clearing, and the HTTP header → ctx extraction.

### Changed

- `LoadConfig` branches on `cfg.Auth.ResolvedAuthMode()`:
  `per_request` → clear server credentials + skip env overrides;
  `server_side` / `hybrid` → existing `applyEnvOverrides` flow.

### Migration notes

- **No action required for existing single-tenant deployments.** Default
  mode is `hybrid`, identical to v2.0.x behavior.
- **For multi-tenant gateways**, set `auth.mode: per_request`, remove
  `sources.<id>.api_key` values, and update clients to pass keys via
  `X-Retrievr-Cred-<source>` headers per request.

## [2.0.1] - 2026-05-10

Patch release. Migrates the 7 cycle-1 E2E pipeline tests to v2 shape
(closing the cycle-3 documented migration debt), wires the cycle-2
`eu_mode` / `audit` / `snapshot` / `enrichment.unpaywall` config blocks
through to the Router (they were declared but never plumbed in cycle 2),
and fixes 4 lint failures + the recurring gofmt CI failure that broke
v1.5.0/v1.6.0/v2.0.0 CI runs.

### Fixed

- **Recurring CI gofmt failure.** v1.5.0–v2.0.0 CI runs failed at the
  `gofmt -l .` step because 22+ files weren't formatted. Re-applied
  `gofmt -w .` and verified clean.
- **Cycle-2 EU-mode + audit wiring gap.** `cmd/retrievr-mcp/main.go` and
  `pkg/retrievr.NewClientFromConfig` now actually plumb the YAML
  `eu_mode:`, `audit:`, `snapshot:`, and `enrichment.unpaywall:` blocks
  through to `internal.NewRouter` via the appropriate `RouterOption`
  values. Before this fix, the config blocks were declared and parsed
  but Router was constructed without them — every `eu_strict`-flagged
  deployment was effectively running in `off` mode at runtime.
- **`internal.ResolveAuditSink` exported** (was `resolveAuditSink`,
  unused — golangci-lint failure on v2.0.0). Now consumed by both the
  MCP main and the library bootstrap.
- **`kindForSource` now honors `Publication.ContentType`** as a
  third-priority Kind derivation. HuggingFace's mixed paper/model/
  dataset emissions correctly map to `KindModel` / `KindDataset` in v2
  Result responses (was always `KindPaper` regardless of `ContentType`).
- **3 `nil` `context.Context` passes** in `resolveS2APIKey` /
  `resolveOAAPIKey` / `resolveHFToken` test call sites replaced with
  `context.TODO()` (staticcheck SA1012 lint failure).
- **7 cycle-1 E2E pipeline tests un-skipped and migrated to v2 Result
  shape.** `TestE2E*PluginFullPipeline` tests now assert on `Kind` +
  `Paper.{DOI, CitationCount, PDFURL, ArXivID}` + `Model` / `Dataset`
  per-kind blocks instead of the v1 flat Publication fields. Provides
  full end-to-end pipeline coverage on the v2 wire format.

### Added

- **`internal.VerifyProvidersSnapshot` called at boot** in both the MCP
  main and `pkg/retrievr.NewClientFromConfig`. Hook #6 of EU mode is
  now actually evaluated rather than being a no-op when `snapshot:`
  config is provided.

### Verification

Local CI sim run (matches `.github/workflows/ci.yaml` step-for-step):
- `go mod tidy` + `git diff --exit-code` — clean
- `go build ./...` — clean
- `go vet ./...` — clean
- `gofmt -l .` — empty (was 22 files in v2.0.0)
- `golangci-lint run ./...` (v1.64.8, the CI version) — clean (was 4
  failures in v2.0.0)
- `go test -race -coverprofile -covermode=atomic ./...` — all suites
  green
- Coverage: **81.4%** (above 80% threshold)

## [2.0.0] - 2026-05-10

Cycle 3 of the v2 multi-cycle plan. **First major release with a stable
public library surface.** Wave-2 synthesized-web provider (Perplexity Sonar)
ships, the cycle-2-deferred HTTP hygiene migration completes, MCP
`rtv_search` defaults to v2 fat-struct shape, and `Client.Stream()` lands
for progressive result delivery.

Wave-2 originally planned Mixedbread + Cohere rerankers; both cut from this
cycle by user direction (Cycle 4+ candidate).

### BREAKING CHANGES

- **`rtv_search` default response shape flips from v1 to v2.** Callers that
  rely on the legacy `Publication` shape now receive `Result` with `Kind`
  discriminator + per-kind data blocks (Paper/Web/Code/...). Migration:
  read `result.kind` first, then access kind-specific fields under
  `result.paper.{doi, citation_count, pdf_url, ...}`,
  `result.web.{site_name, ...}`, `result.code.{repo, stars, ...}`, etc.
  See `docs/library-guide.md` for examples.
- **Explicit `compat: "v1"` returns `RTV_COMPAT_V1_SUNSET`.** The cycle-2
  v1 compat opt-in is removed. Omit the `compat` field for the new
  default, or pass `"v2"` explicitly (idempotent with default).

### Added

- **Perplexity Sonar provider** (`perplexity`) — synthesized web answer
  + inline citations. POST /chat/completions with sonar/sonar-pro/
  sonar-reasoning models. Kinds=[KindWeb], QueryIntents=[QuickLookup,
  DeepResearch]. Maps the synthesized answer to a primary `Result` with
  `LLMContext=<answer>`; citations follow as sparse-shape entries.
  US-resident; blocked under `eu_strict`. Latency ~5-13s — bumps default
  per-source timeout to 20s.
- **`Client.Stream(ctx, params, sources)`** returns `<-chan StreamEvent`
  for progressive result delivery. Per-source results emit as plugins
  return; channel closes when all sources complete or ctx cancelled.
  Trade-offs: no cross-source dedup, no fallback walk. EU-mode gate +
  refusal path + audit event still apply. Not exposed via MCP (MCP
  doesn't stream tool results); CLI exposes via `--stream`.
- **`StreamEvent` type** with `Source`, `Result`, `Err` fields, re-exported
  from `pkg/retrievr` as `retrievr.StreamEvent`.
- **`retrievr-cli search --stream`** flag for progressive output.
- **`ErrCompatV1Sunset` typed sentinel** for callers detecting the v1
  sunset (`errors.Is(err, ErrCompatV1Sunset)`).
- **Comprehensive docs**:
  - `docs/architecture.md` — internals, middleware order, request flow
  - `docs/eu-mode.md` — EU-GDPR mode reference (3 states, 6 hooks)
  - `docs/intents.md` — per-intent semantics + fallback chains
  - `docs/residency.md` — provider residency table + verification policy
  - `docs/library-guide.md` — `pkg/retrievr` API + 6 worked examples
  - `docs/plugin-guide.md` was already present (cycle-1)

### Changed

- **All 10 cycle-1 plugins migrated to `internal.NewEgressClient`** —
  closes the cycle-2 deferred Hook #4 gap. ArXiv, S2, OpenAlex, PubMed,
  Europe PMC, HuggingFace, CrossRef, DBLP, NASA ADS, bioRxiv now share
  the same hygiene contract (neutral User-Agent, no Referer/XFF, no
  cookies) as Wave-1/2 plugins. ~10-line mechanical change per plugin.
- **`SourceCount`** 17 → 18.
- **MCP `rtv_search` `compat` field default** flipped from `"v1"` to
  `"v2"`. Tool definition's enum no longer includes `"v1"`.
- **`ToolDescSearch` rewritten** to describe v2 as canonical. The
  30-second LLM-readable description shipped in cycle 2 is updated to
  mark v1 as sunset.
- **README rewritten** to advertise 18 providers + library-first
  positioning + links to all 6 docs files.

### ADRs added

- ADR-023 (forthcoming): Perplexity citation-mapping pattern
- ADR-024 (forthcoming): v1 sunset rationale + migration path
- ADR-025 (forthcoming): Stream API design — what's intentionally
  out of scope (fallback, dedup) and why

### Tests

- 9 new Perplexity unit tests + live smoke (`TestPerplexity_LiveSmoke`
  passed against real Sonar API in 13.79s).
- 6 new Stream tests covering per-source events, partial-failure
  isolation, ctx-cancellation, EU-mode gate + refusal path, no-sources
  error.
- 3 new sunset tests (`TestSunset_DefaultIsV2`,
  `TestSunset_ExplicitV1ReturnsSunsetError`, `TestSunset_ExplicitV2Works`).

### Known migration debt

- 7 cycle-1 E2E pipeline tests (`TestE2E*PluginFullPipeline`) currently
  `t.Skip("v1 compat sunset in v2.0.0")` pending field-by-field
  migration to v2 Result shape. Substantive coverage exists via the
  cycle-2 `TestRouter_SearchV2WrapsSearch` + the 18 per-provider unit
  test suites; the skipped E2E tests duplicate what's already covered.

### Sign-up gates (cycle 4+)

- Mixedbread (EU-resident reranker) — when a rerank stage is reintroduced
- Cohere (US reranker) — same; auto-disabled in eu_strict per ADR-018
- Tavily, Kagi, You.com — deferred indefinitely per cycle-2 user direction

## [1.6.0] - 2026-05-10

Cycle 2 of the v2 multi-cycle plan (`project_plan/retrievr_v2.md`). Headline:
**Wave-1 providers + EU-GDPR mode + v2 result shape**. Source count grows
from 10 → 17 (Exa, Brave, Linkup, Firecrawl, GitHub, Wikipedia, Unpaywall).
EU-mode platform with all 6 audit hooks ships behind the existing public
API. v2 fat-struct `Result` shape opt-in via MCP `compat: "v2"` arg or
`Client.SearchV2()`; default v1 wire format unchanged for byte-stable
backward compat.

### Added — Wave-1 providers

- **Exa.ai** (`exa`) — neural + keyword web/news search. POST /search with
  `x-api-key`. `Kinds: [web, news]`, `QueryIntents: [quick_lookup, deep_research]`.
  US-resident; blocked under `eu_strict`.
- **Brave Search** (`brave`) — independent 35B+ page index. GET
  /res/v1/web/search with `X-Subscription-Token`. Merges web + news sections
  with `kind` override per result. US-resident; blocked under `eu_strict`.
- **Linkup** (`linkup`) — **EU-resident** web search (Linkup SAS, France)
  with signed DPA. POST /v1/search with Bearer auth. The headline EU-strict
  primary web provider — only Wave-1 source admitted under `eu_strict`.
- **Firecrawl** (`firecrawl`) — web search + per-URL markdown extraction.
  POST /v1/search with Bearer. Cycle-3 will activate the post-merge
  enrichment hook (toggle in config: `enrichment.firecrawl.enabled`).
  US-resident; blocked under `eu_strict`.
- **GitHub Code Search** (`github`) — public repository search via
  GET /search/repositories with PAT. `Kinds: [code]`,
  `QueryIntents: [code_provenance]`. Maps repo metadata (stars, forks,
  language, topics, license, last_commit) into `CodeData`.
- **Wikipedia** (`wikipedia`) — encyclopedia search via the public
  MediaWiki API. Free / no auth (polite User-Agent required).
  `Kinds: [encyclopedia]`, `QueryIntents: [reference, quick_lookup]`.
  Public-research-infrastructure tier; admitted under `eu_strict` only with
  `IncludePublicResearch=true`.
- **Unpaywall** (`unpaywall`) — DOI → OA PDF resolver, wired as a
  **post-merge enrichment hook**. When paper results have a DOI but no
  upstream PDF link, the Router consults Unpaywall to fill `PDFURL` +
  `License` + `OpenAccess`. Toggle via `enrichment.unpaywall` and the
  `Router.WithUnpaywallEnrichment(*UnpaywallPlugin)` option.

### Added — EU-mode platform (all 6 audit hooks per plan §3.7)

- **Hook #1 — Provider residency tags.** `SourcePlugin.Residency() ResidencyTag`
  is now part of the interface; every plugin declares region (EU /
  UK-adequacy / US / public-research-infrastructure / unknown), DPA status
  (signed / covered-by-scc / n/a / unknown), subprocessor URL, and
  last-verified date. Surfaced in `rtv_list_sources`.
- **Hook #2 — Mode gate pre-fanout.** Configurable via
  `Router.WithEUMode(mode, includePublicResearch)`. In `eu_strict` mode,
  non-EU providers are filtered out before fan-out and surface in
  `MergedSearchResult.SourcesSkipped` with `reason: "eu_strict_mode"`.
- **Hook #3 — Outbound query audit log.** Every `Router.Search` call emits
  an `AuditEvent` with `audit_ref`, mode, intent, hashed query (sha256:16,
  default), invoked/skipped/failed providers, fallback flags. Default sink
  routes to slog.Info; opt-in plaintext query via
  `WithAuditLogQueryPlaintext(true)`.
- **Hook #4 — Outbound HTTP hygiene.** New `internal.NewEgressClient(timeout)`
  builds an `*http.Client` with neutral User-Agent (`retrievr/<version> (+repo)`),
  no Referer / X-Forwarded-For / X-Real-IP / Forwarded headers, and no
  cookie jar. All Wave-1 plugins use it; cycle-3 migrates the 10 cycle-1
  scholarly plugins.
- **Hook #5 — Refusal path.** `Router.Search` rejects calls with
  `eu_strict + explicit non-EU sources` upfront with
  `*EUModeProviderConflictError` (satisfies `errors.Is(err, ErrEUModeProviderConflict)`).
  Structured `Requested` / `Blocked` / `Mode` fields let callers render
  remediation messages without parsing strings.
- **Hook #6 — Config drift guard.** `VerifyProvidersSnapshot` computes
  SHA256 of `providers.yaml` and compares to a checked-in signature file.
  Mismatch warns by default; `Strict: true` upgrades to fatal
  (`ErrConfigDriftDetected`). No-op when files unset.

### Added — v2 result shape

- **`Result` fat struct** with `Kind` discriminator (paper / model / dataset /
  web / news / code / encyclopedia) + per-kind data blocks (`PaperData`,
  `WebData`, `CodeData`, etc.). Lives in `internal/rtv.result.go`; aliased
  from `pkg/retrievr/result.go`.
- **`Client.SearchV2(ctx, params, sources)`** returns `*MergedSearchResultV2`
  with `[]Result`. Internally wraps `Search` and runs `PublicationsToResults`.
- **`Router.toResult(p, rank)`** converter — rank-based score
  (`1 / (1 + rank)`), domain auto-derived from URL, snippet auto-truncated
  from Abstract for non-paper kinds, provenance tagged from plugin's
  `Residency()`. 17 SourceMetadata keys for plugins to populate kind-
  specific data.
- **MCP `rtv_search` `compat` arg.** Default `"v1"` keeps the cycle-1 wire
  shape byte-stable. Opt in to `"v2"` for the new fat-struct response.
- **MCP `rtv_search` `intent` arg.** Drives Router source selection +
  fallback chains; values match the `Intent` enum.

### Added — `rtv_list_sources` revamp

`SourceInfo` gains `Kinds`, `QueryIntents`, `Region`, `DPAStatus`,
`SubprocessorURL`, `FreeTier`, `RequiresKey`. LLM agents and operators
can now pick sources by intent + jurisdiction without enumerating booleans.
Updated `ToolDescSearch` to a 30-second LLM-readable description covering
intent / kind / eu_mode / compat semantics.

### Added — config blocks (top-level)

- `eu_mode: { mode, include_public_research }` — gate configuration
- `audit: { enabled, log_query_plaintext, sink }` — Hook #3 controls
- `snapshot: { providers_file, signature_file, strict }` — Hook #6 inputs
- `enrichment: { unpaywall: {...}, firecrawl: {...} }` — post-merge hooks
- `sources: { exa, brave, linkup, firecrawl, github, wikipedia, unpaywall }` —
  7 new provider blocks (all `enabled: false` default; `wikipedia` enabled
  since it's keyless)

### Changed

- **`SourcePlugin` interface** gained `Residency() ResidencyTag` (breaking
  for any external implementor; pre-1.0 acceptable).
- **`SourceCapabilities`** gained `Kinds []ResultKind` (informational; cycle-1
  plugins return empty → converter defaults to `KindPaper`).
- **`MergedSearchResult`** gained `SourcesSkipped`, `AuditRef`,
  `FallbackWalked`, `EUFallbackUsed` (additive, JSON-omitempty preserves
  v1 callers' byte-stable response).
- **`NewRouter`** signature gained variadic `opts ...RouterOption` —
  existing 8-arg callers unaffected. New options: `WithEUMode`,
  `WithAuditSink`, `WithAuditLogQueryPlaintext`, `WithUnpaywallEnrichment`.
- **`SourceCount`** 10 → 17.

### Tests

- 14 new EU-mode conformance tests (`internal/rtv.eumode_test.go`) covering
  every hook end-to-end including HTTP hygiene round-trip + config-drift
  scenarios.
- 8 new converter tests (`internal/rtv.result_convert_test.go`) covering
  paper-default, web-via-SourceMetadata, code with stars, kind override,
  score decay, SearchV2 happy path, snippet truncation.
- 11 new Exa unit tests + live smoke (`TestExa_LiveSmoke`).
- 11 new Brave unit tests + live smoke (`TestBrave_LiveSmoke`).
- 13 new Linkup unit tests + **`TestEUMode_StrictAdmitsLinkupRefusesExa`**
  (the cycle-2 EU-mode end-to-end conformance test) + live smoke.
- 8 new Firecrawl unit tests + live smoke.
- 9 new GitHub unit tests + live smoke.
- 6 new Wikipedia unit tests + live smoke (keyless).
- 9 new Unpaywall unit tests including
  **`TestRouter_EnrichWithUnpaywall_Integration`** that proves the
  post-merge enrichment loop fills missing PDFURLs end-to-end.

### Sign-up gates (cycle 3 / Wave 2 prep)

Wave-2 (cycle 3) needs: Mixedbread (EU-resident reranker, headline EU-mode
companion), Perplexity Sonar (already in `~/code/.creds`), Cohere
(already; auto-disabled in `eu_strict`).

## [1.5.0] - 2026-05-10

Cycle 1 of the v2 multi-cycle plan (`project_plan/retrievr_v2.md`). Headline
goal: extract retrievr's retrieval logic as an importable Go library so liz,
nexus, and other in-process consumers no longer pay the MCP HTTP hop. Cycle
1 is **infrastructure-only** — no new providers, no breaking changes for
MCP consumers. Wave-1 providers (Exa, Brave, Linkup, Firecrawl, Unpaywall,
GitHub, Wikipedia) and the EU-GDPR mode arrive in v1.6.0 (cycle 2).

### Added
- **Public package `pkg/retrievr`** — importable surface with `Client`,
  `Search`, `Get`, `ListSources`, type aliases for every domain type, and
  the new credential / intent / EU-mode types. Cycle-1 escape hatch
  `NewClientFromConfig(configPath, logger)` lets external Go modules wire a
  Client end-to-end with one call. (Cycle 2 replaces this with a richer
  `NewClient(opts ...ClientOption)`.)
- **Context-based credentials.** New `retrievr.WithCredentials(ctx, map[string]string)`
  and `internal.WithCallCredentials(ctx, *CallCredentials)` carry per-call
  API keys keyed by source ID. The legacy `*CallCredentials` typed surface
  remains for the MCP wrapper during cycle 1.
- **Composable plugin-invocation middleware** (`internal/rtv.pluginchain.go`).
  Order outermost → innermost: retry → rate-limit → timeout → plugin.
  Equal-jitter exponential backoff (`RetryConfig`, `DefaultRetryConfig` —
  3 attempts, 250ms base, 8s cap). Each retry attempt acquires its own
  rate-limit token (matches liz DC-145).
- **Intent + per-intent fallback chains.** New `Intent` enum
  (`deep_research`, `quick_lookup`, `primary_source`, `code_provenance`,
  `news`, `reference`) on `SearchParams`. New `RouterFallbackConfig` maps
  intents → primary source set + ordered fallback list. When primary returns
  zero results (or all-fail), router walks the fallback list sequentially
  and short-circuits on the first hit. Cycle-1 default: `academic` chain
  (primary `[s2, openalex]`, fallback `[arxiv, crossref, europmc, pubmed]`)
  mapped to `IntentDeepResearch` and `IntentPrimarySource`.
- **`cmd/retrievr-cli`** — thin standalone CLI built on `pkg/retrievr.Client`.
  Subcommands: `search`, `get`, `sources`. Table or JSON output. Per-call
  API keys from `RETRIEVR_<SOURCEID>_API_KEY` env vars. Stdlib-only (no
  cobra).
- **Result fat-struct stub** (`pkg/retrievr/result.go`). Defines `Result`
  with `Kind` discriminator + per-kind data blocks (`PaperData`, `WebData`,
  `CodeData`, `NewsData`, `ModelData`, `DatasetData`, `EncyclopediaData`).
  Not yet emitted by plugins — plugins still produce `Publication` in cycle
  1; cycle 2 wires the new shape with a v1 `compat: "v1"` MCP shim.
- **EU-mode + audit-sink scaffolding** (`pkg/retrievr/eumode.go`,
  `audit.go`). `EUMode` enum (`off | eu_preferred | eu_strict`), `Region`
  classifications (EU, UK-adequacy, US, public-research-infrastructure,
  unknown), `AuditEvent` + `AuditSink` interface. Stubs only in cycle 1 —
  the gate, mode-filter, six audit hooks, and refusal path land in v1.6.0.
- **`SourceCapabilities.QueryIntents`** — informational field on every
  source's capabilities for intent-tag surfacing via `rtv_list_sources`.
- **Project plan** `project_plan/retrievr_v2.md` (~975 lines) covering
  cycles 1–3, package layout, middleware diagram, EU-mode hooks, risk
  register, sign-up checklist.

### Changed
- **`SourcePlugin` interface** — `Search(ctx, params)` and `Get(ctx, id, include, format)`.
  The `creds *CallCredentials` parameter is removed; plugins read
  credentials from ctx via `internal.CredentialFor(ctx, sourceID, fallback)`.
  All 10 providers (ArXiv, S2, OpenAlex, PubMed, Europe PMC, HuggingFace,
  CrossRef, DBLP, NASA ADS, bioRxiv) migrated. **Breaking** for any
  external Go consumer of the interface, but the package was effectively
  internal-only before this cycle.
- **`Router.searchOneSource` and `Router.Get`** rewritten to invoke plugins
  through the middleware chain. Plugin call latency unchanged in the happy
  path; transient errors now retry with backoff before bubbling up.
- **`RouterConfig.Retry`** (new YAML field, optional) — `RouterRetryConfig`
  with `max_attempts`, `base_delay`, `max_delay`, `jitter_fraction`. Zero
  values fall through to `DefaultRetryConfig`.
- **`RouterConfig.Fallback`** (new YAML field, optional) — `RouterFallbackConfig`
  with per-intent chain definitions. Zero values fall through to
  `DefaultFallbackConfig` (cycle-1 academic chain).
- **`Router.Search`** resolution precedence is now: explicit `Sources` arg
  → `params.Intent` chain lookup → `Router.defaultSources`. Behavior with
  empty `Intent` is byte-identical to v1.1.1.

### Fixed
- `CredentialFor(ctx, …)` is nil-ctx safe — returns the fallback when ctx is
  nil, rather than panicking on the value lookup.

### Tests
- `internal/rtv.pluginchain_test.go` — 15 tests covering chain ordering,
  per-attempt timeout, equal-jitter backoff math, retry-after-N-attempts,
  context-cancellation short-circuit, RouterRetryConfig zero-value
  substitution, transient-error predicate.
- `internal/rtv.fallback_test.go` — 9 tests covering chain primary
  resolution, fallback walk on zero-results / all-fail, no-walk when
  Sources explicit / Intent empty, `DefaultFallbackConfig` shape,
  `resolveFallbackConfig` zero-value substitution.
- `pkg/retrievr/smoke_test.go` — exercises every public identifier
  including the new credentials map, intent surface, and `DefaultFallbackConfig`.

## [1.1.1] - 2026-04-21

### Fixed
- **HTTP routing accepts both `/mcp` and `/mcp/`.** Go's `http.ServeMux` treats `mux.Handle("/mcp", …)` as an exact match and does not canonicalise a trailing slash — requests to `/mcp/` returned `404`. Reverse proxies that normalise empty remaining paths to `"/"` (notably Conduit's `singleJoiningSlash`) always forward as `.../mcp/`, so retrievr appeared dead behind a gateway even when it was perfectly healthy for direct `/mcp` callers. Registering the same `StreamableHTTPServer` handler against `/mcp/` as well closes the gap without changing upstream behavior. One-line change in `rtv.server.go`.

## [1.1.0] - 2026-04-06

### Added
- **CrossRef source plugin** — DOI-centric metadata for 150M+ scholarly works, JATS XML abstract stripping, date-parts conversion, polite pool via mailto
- **DBLP source plugin** — computer science bibliography with 7M+ publications, venue/conference metadata, custom author JSON unmarshaling
- **NASA ADS source plugin** — 16M+ astronomy/astrophysics records, API key auth, parallel array author/affiliation/ORCID mapping, Solr date filtering
- **bioRxiv/medRxiv source plugin** — preprint servers for biology/health sciences, date-range browsing (no keyword search), dual-server support, DOI retrieval
- **Environment variable API key overrides** — `RETRIEVR_{SOURCE}_API_KEY` env vars override YAML config, supports K8s secret injection
- Per-call credential support for NASA ADS (`ads_api_key`)

### Changed
- Source count expanded from 6 to 10
- Tool descriptions updated to list all 10 sources
- BibTeX journal key lookup now includes CrossRef and ADS metadata keys
- Default sources include crossref, dblp, ads (not biorxiv — requires date filter)

## [1.0.2] - 2026-04-06

### Added
- GitHub Actions CI workflow (build, vet, gofmt, golangci-lint, test -race, coverage >= 80%)

### Changed
- README rewritten for public release — fixed response field names, added Claude Code setup section, tighter structure
- MCP tool descriptions rewritten for LLM consumption — now mention concrete output fields
- Integration tests use OpenAlex+EuropePMC for multi-source test (S2 rate limits too aggressive without key)
- S2 integration test skips gracefully on 429/403 instead of failing

## [1.0.1] - 2026-04-06

### Added
- Plugin registry pattern (`rtv.registry.go`) — replaces 6 repetitive init blocks in main.go with data-driven factory map
- BibTeX journal field now checks all source-specific metadata keys (pubmed_journal, s2_journal, emc_journal, oa_venue, arxiv_journal_ref) with priority ordering
- Registry unit tests (`rtv.registry_test.go`) covering factories, initialization, disabled sources, unknown sources
- BibTeX cross-source journal tests covering all source keys and priority ordering

### Fixed
- `errors.Is()` used for `http.ErrServerClosed` comparison in server.go (was using direct equality)
- Dead code in `convertEMCFormat()` — added missing FormatJSON case
- Version test helpers (`SetVersionForTesting`/`ResetVersionForTesting`) protected with mutex against data races
- `TestE2EHuggingFace` race condition — removed erroneous `t.Parallel()` that conflicted with global state mutation
- Log/error constant mixing in router.go — separated `errDetailNoValidSources` from `logMsgNoValidSources`
- `io.LimitReader` int64 cast standardized across ArXiv, S2, and OpenAlex plugins
- `sort.Slice`/`sort.SliceStable` modernized to `slices.SortFunc`/`slices.SortStableFunc` (Go 1.21+)
- `sort.Strings` modernized to `slices.Sort` in router and cache
- Consistent `t.Cleanup(ResetVersionForTesting)` added across all version-mutating tests
- E2E test comment consistency for non-parallel tests

## [1.0.0] - 2026-04-05

### Added
- README.md with installation, configuration, and usage documentation
- LICENSE (MIT)
- ADRs.md documenting key architectural decisions
- docs/tool-reference.md — full reference for all three MCP tools
- docs/plugin-guide.md — guide for implementing new source plugins

### Changed
- Version bumped to 1.0.0

### Fixed
- BibTeX magic string constants extracted to named constants
- Rate limit metric semantics corrected
- Dockerfile `trimpath` flag added for reproducible builds
- Test coverage gaps from DC-11 code review

## [0.11.0] - 2026-03-29

### Added
- BibTeX generation from Publication metadata (`rtv.bibtex.go`), covering all sources
- Prometheus metrics (`rtv.metrics.go`) with a custom registry and nil-safe methods
- `/metrics` endpoint exposing Prometheus metrics
- Metrics: `rtv_search_total`, `rtv_search_duration_seconds`, `rtv_get_total`, `rtv_rate_limit_waits_total`, `rtv_cache_hits_total`, `rtv_cache_misses_total`
- Multi-stage Dockerfile: `golang:1.25-alpine` build → `alpine:3.21` runtime, non-root user, healthcheck
- Integration test suite (`//go:build integration`) for live API validation

## [0.10.0] - 2026-03-22

### Added
- HuggingFace plugin (`rtv.plugin.huggingface.go`) with three sub-sources: papers, models, datasets
- `content_type` routing to dispatch requests to the correct HuggingFace sub-source
- Cross-links between HuggingFace models/datasets and their associated papers

## [0.9.0] - 2026-03-15

### Added
- Europe PMC plugin (`rtv.plugin.europmc.go`) covering 40M+ biomedical publications
- REST/JSON search workflow with full-text XML retrieval support

## [0.8.0] - 2026-03-08

### Added
- PubMed plugin (`rtv.plugin.pubmed.go`) with two-phase XML workflow (ESearch + EFetch)
- MeSH term filtering support
- PMC full-text retrieval support

## [0.7.0] - 2026-03-01

### Added
- OpenAlex plugin (`rtv.plugin.openalex.go`) covering 250M+ scholarly works
- Inverted abstract index reconstruction to plaintext
- Polite pool support (mailto parameter in API requests)

## [0.6.0] - 2026-02-22

### Added
- Semantic Scholar plugin (`rtv.plugin.s2.go`) with citation and reference fetching
- Field selection for Semantic Scholar API requests
- Per-call API key support for Semantic Scholar

## [0.5.0] - 2026-02-15

### Added
- ArXiv plugin (`rtv.plugin.arxiv.go`) — first real source plugin
- ArXiv Atom XML API integration with search field mapping, date filtering, and pagination

## [0.4.0] - 2026-02-08

### Added
- MCP server with Streamable HTTP transport on `/mcp` (port 8099) (`rtv.server.go`)
- Three MCP tools: `rtv_search`, `rtv_get`, `rtv_list_sources` (`rtv.tools.go`)
- Request ID injection middleware (`rtv.middleware.go`)
- Per-tool logging middleware
- Graceful shutdown with configurable timeout

## [0.3.0] - 2026-02-01

### Added
- Source router with concurrent fan-out search across all requested plugins (`rtv.router.go`)
- Result merging with exact-match deduplication by DOI and ArXiv ID
- Round-robin interleaving for relevance sorting across sources
- Partial failure handling: working sources return results, failed sources reported in `sources_failed`
- Plugin contract test suite exercising every SourcePlugin implementation

## [0.2.0] - 2026-01-25

### Added
- Per-source token-bucket rate limiting (`rtv.ratelimit.go`) via `golang.org/x/time/rate`
- Per-credential rate limit buckets keyed by credential hash, TTL-evicted after 15 min inactivity
- Credential resolution (`rtv.credentials.go`) with priority order: per-call > server config > anonymous
- In-memory LRU cache with TTL (`rtv.cache.go`)

## [0.1.0] - 2026-01-18

### Added
- Go module scaffold (`go.mod`, `go.sum`)
- Unified types: `Publication`, `Author`, `SearchParams`, and related structs (`rtv.types.go`)
- `SourcePlugin` interface definition (`rtv.plugin.go`)
- YAML config loading with `go-playground/validator` struct validation (`rtv.config.go`)
- Sentinel error variables and constant error message strings (`rtv.errors.go`)
- Thread-safe version loading from `versions.yaml` or ldflags via `sync.Once` (`rtv.version.go`)
