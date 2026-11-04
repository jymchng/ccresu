// Package configs carries the example configuration that ships with ccresu.
//
// The YAML file next to this comment is the single source of truth for three
// consumers: the release archive (which ships it so the installer can install it
// as the user's configuration), `scripts/install.sh` when it runs from a
// checkout, and `ccresu config init`, which embeds it here. Embedding means the
// binary can create a valid configuration without a checkout or a release tarball
// beside it, and it means the example a user gets from `config init` is
// byte-for-byte the one the installer would have copied.
package configs

import _ "embed"

// Example is the contents of config.example.yaml.
//
//go:embed config.example.yaml
var Example []byte
