// Package internal collects implementation details that pkg/retrievr depends
// on but does not expose. Cycle-1 status: empty placeholder. Cycle 1 task #3
// moves the existing internal/rtv.cache.go, rtv.ratelimit.go, and parts of
// rtv.router.go (dedup, fanout, idparse) into subpackages here:
//
//	pkg/retrievr/internal/rate/      token-bucket per (sourceID, credKey)
//	pkg/retrievr/internal/cache/     LRU response cache
//	pkg/retrievr/internal/dedup/     DOI / ArXiv-ID / canonical-URL dedup
//	pkg/retrievr/internal/fanout/    bounded errgroup dispatcher
//	pkg/retrievr/internal/bibtex/    BibTeX assembly (moved verbatim)
//	pkg/retrievr/internal/idparse/   prefixed-ID parse / unparse
//	pkg/retrievr/internal/httpx/     egress HTTP client (neutral UA, no PII headers)
//	pkg/retrievr/internal/rerank/    post-merge reranker stage (cycle 3)
//
// Until then, the existing internal/rtv.*.go files in the repository root
// remain authoritative and the public Client wraps them via NewClientFromRouter.
package internal
