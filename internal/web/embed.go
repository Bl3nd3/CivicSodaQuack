// Copyright (c) 2026 Neomantra Corp

package web

import "embed"

// assetsFS carries the browser UI into the binary, so `csq web` needs no
// install step, no node toolchain, and no files next to the executable — the
// same single-binary property the rest of csq has.
//
//go:embed assets
var assetsFS embed.FS
