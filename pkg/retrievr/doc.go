// Package retrievr is the public, importable API for the retrievr retrieval
// library. It aggregates academic, web, code, and reference sources behind a
// unified Result type and a small surface (Search, Get, ListSources).
//
// Two consumption modes:
//
//  1. Direct in-process import (preferred for liz / nexus and other Go
//     services): build a *Client via NewClient and call Search/Get directly,
//     skipping any MCP transport.
//
//  2. MCP server (cmd/retrievr-mcp): a thin wrapper around this package that
//     translates JSON tool calls into Client method invocations.
//
// Cycle-1 status: this package is a SKELETON. Most types are aliases for the
// existing internal package so the import path is stable; logic still lives
// in internal/ and is not yet refactored. Tasks #2 (ctx-credentials), #3
// (middleware pipeline), and #4 (intent/fallback chains) of the v1.5.0 plan
// progressively move logic here. See project_plan/retrievr_v2.md.
package retrievr
