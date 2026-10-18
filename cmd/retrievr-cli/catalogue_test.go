package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
)

func TestRunCatalogue_WritesTheCatalogueDocument(t *testing.T) {
	var buf bytes.Buffer
	if code := runCatalogue(&buf, nil); code != exitCodeSuccess {
		t.Fatalf("exit = %d, want %d", code, exitCodeSuccess)
	}
	want, err := retrievr.CatalogueJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatal("catalogue subcommand output differs from retrievr.CatalogueJSON()")
	}
	var doc retrievr.CatalogueDocument
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Sources) == 0 {
		t.Fatal("empty catalogue")
	}
}

func TestRunCatalogue_RejectsPositionalArgs(t *testing.T) {
	var buf bytes.Buffer
	if code := runCatalogue(&buf, []string{"extra"}); code != exitCodeUsage {
		t.Fatalf("exit = %d, want %d", code, exitCodeUsage)
	}
	if buf.Len() != 0 {
		t.Fatal("nothing must be written on a usage error")
	}
}

// The catalogue must not need a config file: run() with a config path that
// does not exist still succeeds for this subcommand.
func TestRun_CatalogueNeedsNoConfig(t *testing.T) {
	if code := run([]string{"--config", "/nonexistent/retrievr.yaml", subcommandCatalogue}); code != exitCodeSuccess {
		t.Fatalf("exit = %d, want %d", code, exitCodeSuccess)
	}
}
