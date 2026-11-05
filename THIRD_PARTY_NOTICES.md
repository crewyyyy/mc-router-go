# Upstream provenance

This is a deliberately reduced derivative, not an unmodified itzg/mc-router
release and not a claim that upstream provides Linux transparent source binding.

- Upstream: https://github.com/itzg/mc-router
- Reviewed/pinned commit: `63534c550395a4b54ac4372fb8f2d108ee1bd213`
- License: MIT, Copyright (c) 2018-2025 Geoff Bourne. Preserved in LICENSE.txt.
- Adapted code/behavior: `mcproto/decode.go` (Minecraft handshake field order and
  Forge NUL-delimited hostname metadata), `server/routes.go`'s
  `NormalizeServerAddress` (Forge/infinity-filter metadata, trailing root dot,
  case normalization, modern `_o` origin query), `server/connector.go`
  (absolute handshake deadline, static hostname-to-backend routing, exact
  inspected-byte replay, independent bidirectional TCP pumps).

The connector and framing were rewritten rather than copied blindly: frame
allocation has a strict 4 KiB bound, reads use io.ReadFull, VarInts are checked
for 32-bit overflow, handshake UTF-16 and UTF-8 lengths are separately checked,
Unicode route keys canonicalize through IDNA, and all logging is JSON.

Added here: admission before goroutine creation, bounded per-IP state and token
buckets, write/idle/half-close/shutdown deadlines, Linux IP_TRANSPARENT backend
sockets, fail-closed IPv4 behavior, namespace/network deployment and regression
tests. No Docker/Kubernetes/ngrok discovery, public control API, autoscaling,
login packet parsing, PROXY header acceptance or player-identity forwarding is
included. This is not a replacement for all upstream features.

Go module dependencies and their exact checksums are recorded in go.mod/go.sum.
The Go runtime/standard library, `golang.org/x/net/idna`, and its
`golang.org/x/text` dependency share the BSD license reproduced in
`third_party/GO-BSD-LICENSE.txt`. Keep this license, LICENSE.txt, and this
notice alongside a redistributed binary.
