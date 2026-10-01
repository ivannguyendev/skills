# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

A collection of Claude Code agent skills. Each top-level directory is one skill (`brainstorming`, `commit`, `docker`, `go-lang`, `websocket`). There is no root build system. The only code you can run is the Go module at `go-lang/assets/service-skeleton`.

## Skill anatomy

- `SKILL.md`: YAML frontmatter with `name` and `description`, then the body. The `description` is the trigger, so it lists the tasks, the symptoms and the user phrasings (including Vietnamese) that should activate the skill. Other frontmatter fields vary: community-sourced skills (`brainstorming`, `docker`) use top-level `risk`/`source`/`date_added`, and `go-lang` uses a `metadata:` block with `version` and `tested-with`.
- `references/*.md`: topic files loaded on demand. `SKILL.md` ends with a "load only when" map table. When you add, rename or remove a reference, update that table. Long references open with a `## Contents` section.
- `assets/`: only compile-checked code or config that is hard to get right.
- Body pattern: decision tables, MUST / MUST NOT rules, a traps or "corrections to outdated advice" table, and step-by-step workflows with exit gates. Prefer this to prose.
- Keep skills lean. Their value is opinionated decisions, rules, pitfalls and verified current API facts, not large app templates. Before adding an asset or reference, check that it earns its maintenance cost.

## File naming

- Markdown and YAML: kebab-case (`resilience-and-load.md`).
- Go: snake_case per Go convention (`grpc_server.go`, `error_mapping_test.go`). Avoid suffixes that Go reads as build constraints (`_linux`, `_amd64`, …).

## go-lang skeleton: commands

Run these from `go-lang/assets/service-skeleton`:

```sh
go build ./...
go test -race -shuffle=on ./...                      # full suite as CI runs it
go test -race -run TestNoGoroutineLeakUnderLoad ./internal/httpserver/   # single test
go test -run 'Allocs$' ./internal/resilience/ ./internal/httpserver/     # alloc budgets: //go:build !race, so run WITHOUT -race
go test -bench . -run '^$' ./internal/resilience/                        # benchmarks
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...   # golangci-lint is not installed locally
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
docker build -t skeleton:dev .
```

Other assets:
- `go-lang/assets/proto-templates`: `buf lint`, `buf breaking`, `buf generate` (buf v2 config; buf is not installed locally). The skeleton does not import the generated `gen/` code; the templates stand alone.
- `go-lang/assets/observability`: `docker compose -f docker-compose.yaml up -d` starts a local OTel Collector (OTLP on 4317/4318) and Jaeger v2 (UI on 16686), bound to loopback.

## go-lang skeleton: invariants

- The skeleton must build, lint with zero issues against its own `.golangci.yaml` (golangci-lint v2), and pass `go test -race`, including the leak tests and allocation budgets. `go-lang/SKILL.md` promises all three.
- `metadata.tested-with` in `go-lang/SKILL.md` records the verified Go, grpc-go, otel, chi, golangci-lint and buf versions. When you bump `go.mod`, update it, and update any version-specific claims in SKILL.md and the references (for example, the govulncheck note about grpc-go v1.84.0 vs v1.83.2).
- `example.com/skeleton` is a placeholder that the rename recipe in SKILL.md ("Using the skeleton") rewrites. It also appears in `.golangci.yaml` (`goimports.local-prefixes`) and `proto-templates/buf.gen.yaml` (`go_package_prefix`). Keep all of them in sync.
- Keep the `buf.gen.yaml` plugin versions in step with `google.golang.org/protobuf` and `google.golang.org/grpc` in `go.mod`.
- The package map and startup flow (`main → config.validate → newServers → lifecycle`) are documented in the "Using the skeleton" section of `go-lang/SKILL.md`. Update that section when the layout changes.

## Commits

Use `commit/SKILL.md`: Conventional Commits with this repo's types (`ref` for refactors, `update` for small content changes), a bulleted body, and no AI attribution lines. It refuses to commit on `main`/`master` unless the user explicitly says so.
