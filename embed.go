// Package qinglong exposes the embedded front-end build so that the whole
// panel can be shipped as a single executable.
package qinglong

import "embed"

// WebFS holds the React build output copied from the Node project's
// static/dist directory. It is embedded verbatim; the Go side never rewrites
// front-end assets.
//
//go:embed all:web
var WebFS embed.FS

// NodeModulesFS holds the vendored Node runtime the ql API needs (@grpc/grpc-js,
// @grpc/proto-loader, protobufjs, undici, nodemailer, ...). shell/preload/client.js
// requires these at the top of the file, so a deployment without node_modules
// loses QLAPI for every JavaScript task.
//
// It is extracted to <QL_DIR>/node_modules on first boot (internal/nodemodules):
// missing files only, never overwriting an existing Node installation.
//
//go:embed all:node_modules
var NodeModulesFS embed.FS

// VersionYAML is the release manifest shipped beside the binary. The file on
// disk wins when it exists (a user may drop in a newer one), but the embedded
// copy keeps /api/health and the version endpoint populated in a distribution
// that only contains the executable.
//
//go:embed version.yaml
var VersionYAML []byte
