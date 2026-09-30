# Agent instructions

## Build and verification

- Format Go code with `gofmt -w ./cmd ./internal`.
- Run `go vet ./...` and `go test ./...`.
- E2E with a real Devin login is opt-in: on Windows set `AGENT_BRIDGE_E2E=1` and `DEVIN_EXE` to the CLI executable. The test invokes Devin with a clean environment and `swe-2-medium`; preserve its artifacts under `artifacts/`. Add `AGENT_BRIDGE_E2E_REWORK_ONLY=1` to run only the Stop, permission-denial, and question scenarios.
- Cross-compile the CLI for Windows, macOS, and Linux on amd64 and arm64. Keep cross-compiled binaries outside the source tree or under ignored `artifacts/`.

## Design constraints

- Keep the daemon independent of channel implementations; it depends only on `internal/channel.Channel`.
- Do not log Telegram bot tokens or complete agent payloads.
- Hook handlers fail open and write no diagnostic text to stdout.
- User-facing output and documentation are in Brazilian Portuguese. Do not add emojis.
