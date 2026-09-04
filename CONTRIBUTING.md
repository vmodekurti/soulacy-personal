# Contributing to Soulacy

Thanks for your interest in improving Soulacy! This guide covers the basics.

## Development setup

Requirements:

- **Go 1.26.6+** (CGO enabled — SQLite is compiled in)
- **Node 18+** (for the GUI under `gui/`)
- **Python 3.10+** (for the executor and the experimental Python SDK)

```bash
git clone https://github.com/vmodekurti/soulacy-personal.git
cd soulacy
make build      # builds the soulacy + sy binaries
make test       # runs the Go test suite with the race detector
```

## Branching & commits

- Branch off `main`. Use a descriptive branch name (e.g. `fix/session-eviction`).
- Keep commits focused; write imperative commit subjects (`gateway: ...`,
  `runtime: ...`) matching the existing history.
- Open a pull request against `main`. CI must be green before merge.
- Sign off every commit with `git commit -s`. The sign-off certifies the
  [Developer Certificate of Origin](DCO.md); pull requests with unsigned
  commits are rejected by the source-availability check.

## Code standards

- Run `make lint` and `gofmt -w` before pushing; CI enforces both.
- Add or update tests for behavior changes. Pure-Go tests should not require a
  live LLM or network — follow the style in `internal/runtime/engine2_test.go`.
- For Go changes touching tools or the gateway, run `go test -race ./...`.

## Reporting bugs & requesting features

Open a GitHub issue with reproduction steps and your environment details. For
**security** issues, follow [SECURITY.md](SECURITY.md) instead of filing a
public issue.

## License

By contributing, you certify the [Developer Certificate of Origin](DCO.md).
Accepted contributions are licensed under the project's Apache-2.0 license.
The project name and logos are governed separately by
[TRADEMARKS.md](TRADEMARKS.md).
