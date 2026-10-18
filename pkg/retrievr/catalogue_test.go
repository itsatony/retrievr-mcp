package retrievr_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
)

// committedCataloguePath is docs/source-catalogue.json relative to this package.
var committedCataloguePath = filepath.Join("..", "..", "docs", "source-catalogue.json")

// TestCommittedCatalogueMatchesCatalogue is the drift guard: the committed
// docs/source-catalogue.json must be byte-identical to CatalogueJSON(). On
// failure run `make catalogue` and commit the result.
func TestCommittedCatalogueMatchesCatalogue(t *testing.T) {
	committed, err := os.ReadFile(committedCataloguePath)
	if err != nil {
		t.Fatalf("read %s: %v", committedCataloguePath, err)
	}
	fresh, err := retrievr.CatalogueJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(committed, fresh) {
		t.Fatalf("%s is stale — run `make catalogue` and commit it", committedCataloguePath)
	}
}

// The public catalogue needs no Client: a zero-value program can render it.
func TestCatalogue_PublicSurfaceWithoutClient(t *testing.T) {
	cat := retrievr.Catalogue()
	if len(cat) < 61 {
		t.Fatalf("catalogue has %d sources, want >= 61", len(cat))
	}
	seen := map[string]bool{}
	for _, e := range cat {
		if seen[e.ID] {
			t.Fatalf("duplicate id %q", e.ID)
		}
		seen[e.ID] = true
		if retrievr.SourceCategoryFor(e.ID) != e.Category {
			t.Fatalf("%s: SourceCategoryFor disagrees with the entry", e.ID)
		}
	}
	if !seen[retrievr.SourceArXiv] {
		t.Fatal("arxiv missing")
	}
	if got := len(retrievr.AllSourceCategories()); got != 9 {
		t.Fatalf("categories = %d, want 9", got)
	}
	if retrievr.CatalogueSchemaVersion == "" {
		t.Fatal("schema version empty")
	}
}

// ListSources on a real client agrees with the catalogue on the governance
// fields, for every source the client has registered.
func TestCatalogue_AgreesWithClientListSources(t *testing.T) {
	client, closeFn, err := retrievr.NewClientFromConfigBytes([]byte(bytesConfigYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	byID := map[string]retrievr.CatalogueEntry{}
	for _, e := range retrievr.Catalogue() {
		byID[e.ID] = e
	}
	infos := client.ListSources(context.Background())
	if len(infos) == 0 {
		t.Fatal("client lists no sources — the comparison would be vacuous")
	}
	for _, info := range infos {
		e, ok := byID[info.ID]
		if !ok {
			t.Fatalf("ListSources reports %q, which the catalogue lacks", info.ID)
		}
		if e.Category != info.Category || e.Region != info.Region || e.RequiresAPIKey != info.RequiresKey || e.SupportsGet != info.SupportsGet {
			t.Fatalf("%s: catalogue and ListSources disagree", info.ID)
		}
	}
}

func TestPublicErrorReExports(t *testing.T) {
	qe := &retrievr.QuotaExhaustedError{Source: "youtube", Reason: "quotaExceeded"}
	if !errors.Is(qe, retrievr.ErrQuotaExhausted) || !errors.Is(qe, retrievr.ErrRateLimitExceeded) {
		t.Fatal("QuotaExhaustedError must match both sentinels")
	}
	if retrievr.ClassifySourceError(qe) != retrievr.SourceErrQuotaExhausted {
		t.Fatal("quota must classify as quota_exhausted")
	}
	if retrievr.ClassifySourceError(retrievr.ErrRateLimitExceeded) != retrievr.SourceErrRateLimited {
		t.Fatal("throttle must classify as rate_limited")
	}
	for _, e := range []error{retrievr.ErrCredentialRequired, retrievr.ErrCredentialInvalid, retrievr.ErrAllSourcesFailed} {
		if e == nil {
			t.Fatal("nil re-export")
		}
	}
}
