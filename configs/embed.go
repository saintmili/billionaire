// Package configs ships the initial application configuration with the binary.
package configs

import _ "embed"

// Example is a paper-only configuration with environment references for secrets.
//
//go:embed example.yaml
var Example string
