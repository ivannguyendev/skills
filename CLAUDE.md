# CLAUDE.md

Operational memory and engineering principles for Claude Code (and AI agents) working within this repository.

## 1. Repository Purpose & Architecture

- **Core Identity**: This repository is a centralized collection of production-grade AI skills designed for autonomous agents (Claude Code, Antigravity, Cursor, Windsurf).
- **Design Philosophy**: Skills are opinionated operational playbooks, hard engineering constraints, and battle-tested traps—**not** generic documentation, tutorial blogs, or boilerplate dumps.
- **Skill Structure**: Each top-level directory represents an independent skill:
  - `brainstorming`: Structured technical exploration, trade-off analysis, and decision logging.
  - `commit`: Conventional Commits standard with task breakdown and branch safety.
  - `go-lang`: High-performance Go microservices, concurrency safety, and leak prevention.
  - `javascript`: Production Node.js & browser runtimes, event-loop lag, timeouts, and memory management.
  - `typescript`: Honest type modeling, boundary validation, and build/compiler diagnostics.
  - `mongo`: Schema locality, ESR index optimization, zero COLLSCAN, and pool sizing.
  - `docker`: Secure multi-stage images, layer caching, and minimal attack surface.
  - `websocket`: Resilient realtime connections, ping/pong heartbeats, and backpressure handling.

## 2. Skill Authoring Standards

When adding or refactoring skills in this repository, strictly adhere to these standards:

### Frontmatter & Trigger Design
- Every skill entrypoint is `SKILL.md` with YAML frontmatter containing `name` and `description`.
- The `description` acts as the semantic activation trigger for agents. It **must** detail:
  - Core problem domains and tasks.
  - Production symptoms (e.g., `COLLSCAN`, `100% CPU`, `memory leak`, `event-loop lag`).
  - Common user phrasings in both **English and Vietnamese** (e.g., `tìm kiếm mờ`, `lỗi connection`, `đánh index`).

### Body Layout & Content Rules
- **Prefer Tables & Decision Matrices**: Use tables, MUST / MUST NOT rules, and trap corrections over prose.
- **On-Demand Loading (`references/`)**: Detailed guides must live under `references/`. The `SKILL.md` file must contain a lookup table specifying when each reference file is loaded. Keep the root skill file compact.
- **Lean Assets**: The `assets/` directory is reserved solely for compile-checked code, testable configs, or templates that are error-prone to recreate from scratch.
- **File Naming**:
  - Markdown/YAML files: `kebab-case.md` (e.g., `query-performance.md`).
  - Code files: follow language idioms (e.g., `snake_case.go` for Go).

## 3. Verification & Code Quality Invariants

- **Zero-Regressions in Assets**: Any code sample or asset provided in a skill must represent verified, production-ready code.
  - For `go-lang/assets/service-skeleton`: Must build cleanly, pass `golangci-lint` without exceptions, and pass `go test -race` with zero goroutine leaks and allocation budgets intact.
- **Current & Verified Tooling**: Maintain exact, tested version strings in skill metadata (e.g., Go, Node, MongoDB driver, linters). When updating recommendations, ensure breaking syntax or deprecated APIs are called out in the respective traps table.

## 4. Agent Operating Guidelines

- **Language Policy**: Respond in the user's conversation language. When the user writes in Vietnamese, reply in Vietnamese with standard English technical terms (API, goroutine, index, cache, retry...).
- **Minimal Back-and-Forth**: Act like a senior engineer briefed by a lead—read code/skills first, formulate a strong recommendation, explain assumptions clearly, and ask only pivotal questions if choices are irreversible.
- **Git & Commits**: Always invoke and adhere to `commit/SKILL.md`. Use Conventional Commits (`feat`, `fix`, `ref`, `docs`, `update`), generate concise bullet-point summaries, and never commit directly to `main`/`master` without explicit user confirmation.
