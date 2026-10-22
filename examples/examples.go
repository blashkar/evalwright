// Package examples embeds the example suites, so the CLI can scaffold them
// with `evalwright init` and tests can check that every example stays valid.
package examples

import "embed"

// FS holds every example suite (*.yaml) in this directory.
//
//go:embed *.yaml
var FS embed.FS

// Quickstart is the suite `evalwright init` writes.
const Quickstart = "quickstart.yaml"
