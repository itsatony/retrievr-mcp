package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
)

const (
	flagNameSources = "sources"
	flagNameLimit   = "limit"
	flagNameFormat  = "format"
	flagNameIntent  = "intent"
	flagNameSort    = "sort"
	flagNameStream  = "stream"

	formatTable = "table"
	formatJSON  = "json"

	defaultSearchLimit = 10

	usageSearch = `retrievr-cli search — search across one or more sources.

Usage:
  retrievr-cli search [flags] <query>

Flags:
  --sources <a,b,c>   Comma-separated source IDs (e.g. arxiv,s2). When omitted,
                      Router uses defaults or, when --intent is set, the
                      configured chain primary set.
  --intent <name>     deep_research | quick_lookup | primary_source |
                      code_provenance | news | reference. Selects a chain
                      from the router's fallback config.
  --limit <N>         Max merged results (default 10).
  --sort <name>       relevance | date_desc | date_asc | citations.
  --format <name>     table (default) or json.

Per-call API keys are picked up from RETRIEVR_<SOURCEID>_API_KEY env vars.
Example: RETRIEVR_S2_API_KEY=… retrievr-cli search --intent=deep_research "transformer attention"
`
)

func runSearch(ctx context.Context, client *retrievr.Client, args []string) int {
	fs := flag.NewFlagSet(subcommandSearch, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageSearch) }

	sourcesCSV := fs.String(flagNameSources, "", "")
	intent := fs.String(flagNameIntent, "", "")
	limit := fs.Int(flagNameLimit, defaultSearchLimit, "")
	sort := fs.String(flagNameSort, "", "")
	format := fs.String(flagNameFormat, formatTable, "")
	stream := fs.Bool(flagNameStream, false, "")

	if err := fs.Parse(args); err != nil {
		return exitCodeUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return exitCodeUsage
	}
	query := strings.Join(rest, " ")

	if *intent != "" && !retrievr.IsValidIntent(*intent) {
		fmt.Fprintf(os.Stderr, "retrievr-cli: invalid --intent %q\n", *intent)
		return exitCodeUsage
	}

	var sources []string
	if *sourcesCSV != "" {
		sources = splitCSV(*sourcesCSV)
	}

	params := retrievr.SearchParams{
		Query:  query,
		Limit:  *limit,
		Intent: retrievr.Intent(*intent),
	}
	if *sort != "" {
		params.Sort = retrievr.SortOrder(*sort)
	}

	if *stream {
		return runStreamingSearch(ctx, client, params, sources)
	}

	result, err := client.Search(ctx, params, sources)
	if err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: search failed: %v\n", err)
		return exitCodeError
	}

	switch *format {
	case formatJSON:
		if err := writeJSON(os.Stdout, result); err != nil {
			fmt.Fprintf(os.Stderr, "retrievr-cli: encode result: %v\n", err)
			return exitCodeError
		}
	case formatTable:
		writeSearchTable(os.Stdout, result)
	default:
		fmt.Fprintf(os.Stderr, "retrievr-cli: invalid --format %q\n", *format)
		return exitCodeUsage
	}
	return exitCodeSuccess
}

func writeSearchTable(w *os.File, result *retrievr.MergedSearchResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer func() { _ = tw.Flush() }()

	fmt.Fprintf(tw, "ID\tSOURCE\tYEAR\tTITLE\n")
	for _, pub := range result.Results {
		title := pub.Title
		if len(title) > 80 {
			title = title[:77] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", pub.ID, pub.Source, firstTen(pub.Published), title)
	}
	fmt.Fprintf(tw, "\nTotal: %d  Queried: %s  Failed: %s  HasMore: %t\n",
		result.TotalResults,
		strings.Join(result.SourcesQueried, ","),
		strings.Join(result.SourcesFailed, ","),
		result.HasMore,
	)
}

func writeJSON(w *os.File, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// runStreamingSearch consumes Client.Stream and prints per-source events
// as they arrive. Useful for slow providers (e.g., Perplexity Sonar at
// 5–13s median) where progressive rendering beats waiting for a full merge.
func runStreamingSearch(ctx context.Context, client *retrievr.Client, params retrievr.SearchParams, sources []string) int {
	ch, err := client.Stream(ctx, params, sources)
	if err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: stream failed: %v\n", err)
		return exitCodeError
	}
	for ev := range ch {
		if ev.Err != nil {
			fmt.Fprintf(os.Stderr, "[%s] error: %v\n", ev.Source, ev.Err)
			continue
		}
		if ev.Result == nil {
			continue
		}
		for _, pub := range ev.Result.Results {
			title := pub.Title
			if len(title) > 80 {
				title = title[:77] + "..."
			}
			fmt.Printf("[%s] %s\t%s\n", ev.Source, pub.ID, title)
		}
	}
	return exitCodeSuccess
}

// firstTen returns the first 10 chars of s (suitable for YYYY-MM-DD dates),
// or s itself when shorter. Empty string passes through.
func firstTen(s string) string {
	if len(s) <= 10 {
		return s
	}
	return s[:10]
}
