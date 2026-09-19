# Retired CI workflows

GitHub Actions is **retired** for this repository by operator decision (2026-09-19).
This file is not parked pending a re-enable — it is kept only as a record. GitHub Actions
only triggers workflow files under `.github/workflows/`, so nothing in this directory runs.

## Everything runs locally

```
make ci
```

## What was parked

`ci.yaml` ran a single `build-and-test` job of seven steps. `make ci` runs the same seven,
in the same order — it is labelled in the Makefile as "simulate the full GitHub CI pipeline
locally" and its steps are numbered `[1/7]`…`[7/7]` to match.

| CI step | What it ran | Local coverage |
|---|---|---|
| 1 | `go mod tidy` + `git diff --exit-code go.mod go.sum` | `make ci` `[1/7]` |
| 2 | `go build ./...` | `make ci` `[2/7]` |
| 3 | `go vet ./...` | `make ci` `[3/7]` |
| 4 | `gofmt -l .` must be empty | `make ci` `[4/7]` → `fmt-check` |
| 5 | golangci-lint `v1.64.8` over `./...` | `make ci` `[5/7]` → `make lint`, which pins **the same** `v1.64.8` (`GOLANGCI_LINT_VERSION` in the Makefile) and installs it if the version on `PATH` does not match |
| 6 | `go test -race -coverprofile -covermode=atomic ./...` | `make ci` `[6/7]` |
| 7 | coverage floor of **80%** over the total | `make ci` `[7/7]`, same 80% floor (`COVERAGE_MIN`) |

The lint version is the one place where a local gate usually drifts from CI, and here it does
not: the Makefile pins the identical version and reinstalls on mismatch.

## What this does not cover

- **A clean-checkout run on a fresh machine.** CI checked out the repo from scratch on an
  ubuntu-latest runner. A developer box carries build outputs, caches and a populated module
  cache, so a regression visible only from a bare clone — a missing tracked file, an
  ungitignored build artefact — has no gate now.
- **The pinned Go toolchain.** CI resolved Go from `go.mod` via `setup-go`'s
  `go-version-file`. Locally whatever `go` is on `PATH` is used; it is normally the same, but
  nothing asserts it.

No multi-OS or multi-Go-version matrix existed, and no coverage service (codecov or similar)
was ever uploaded to — the 80% floor was asserted inline and still is — so nothing of that
kind is lost.
