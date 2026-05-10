// Package plugin defines the SourcePlugin interface and the supporting types
// (Middleware, Registry, ResidencyTag) that providers implement to participate
// in retrievr's fan-out search.
//
// Cycle-1 status: SourcePlugin is re-exported as an alias for the existing
// internal.SourcePlugin so external code can reference the public type. The
// signature still carries *CallCredentials; cycle 1 task #2 drops that
// parameter and switches plugins to read credentials from ctx.
package plugin

import (
	"github.com/itsatony/retrievr-mcp/internal"
)

// SourcePlugin is the contract every provider implements. See
// internal.SourcePlugin for the interface body.
//
// v2 changes (cycle 1 task #2): the creds parameter is removed; plugins call
// retrievr.CredentialFor(ctx, "<id>") instead. A new Residency() method
// returns a ResidencyTag (cycle 2 — eu-mode hook #1).
type SourcePlugin = internal.SourcePlugin
