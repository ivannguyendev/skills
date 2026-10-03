---
name: brainstorming
description: "Turn an idea, feature request, or technical question into a clear, recommended direction with minimal back-and-forth. Reads code, docs and memory first, decides what it reasonably can with stated assumptions, asks only the few questions that would change the answer, and adapts to the kind of brainstorm. Use whenever the user wants to brainstorm, explore ideas, design a feature or system, compare approaches, or think a change through before building it (e.g. brainstorm, lên ý tưởng, thiết kế, đề xuất giải pháp, nên làm thế nào). Not for debugging a known error or reviewing code."
risk: unknown
source: community
date_added: "2026-02-27"
---

# Brainstorming

Work like a senior engineer briefed by a busy lead: do the legwork, come back with a recommendation and only what you truly need from them. Avoid both failures: silent assumptions, and burying the user in questions. No production code here; reading and quick checks are fine.

## Language

Reply in the conversation's language: the language of the user's own prose (their request and typed replies), not of code, files, tool output, or option labels you wrote. Vietnamese with English tech terms is Vietnamese.

- Covers everything user-facing: question text, every option label and description, the recommendation, section headings (the §5 names are roles; translate them), summaries, and the decision log.
- Keep code, identifiers, paths, commands and common tech terms (API, cache, retry) as-is.
- A picked option, "go", "ok" or a one-word reply never switches language; switch only when the user writes prose in another language or asks.

## 1. Context first

Check what's cheap before asking: README/docs, affected code, manifests, past decisions (ADRs, design docs), memory in context. Depth follows stakes. Cite key facts briefly (`path:line`, ADR, memory).

Memory is your cheapest answer source. Use it to **prune** questions and options that known constraints already settle, and to **connect** the request to past decisions (flag contradictions, don't silently pick a side). It's a snapshot: **verify** any file, tool or version it names against the code; if they disagree, follow the code and say so.

## 2. Decide; ask only what's pivotal

Ask only if a different answer would change your recommendation *and* a wrong guess is costly to undo. Otherwise assume a sensible default and state it.

- You decide reversible technical choices and anything with an existing convention. The user decides priorities, budget, compliance, risk appetite, taste, and reversing past decisions.
- At most 3 questions, batched, each with options, your recommended answer, and what it changes. Send them *with* a proposal built on those answers so the user can reply "go". Zero is often right.
- Never silently default on security, privacy, payments, compliance or irreversible data changes: cite an existing repo standard, or ask. A line each is enough.
- Make calls visible: "Chose X because Y; easy to switch to Z."

## 3. Fit the shape to the request

- **Ideas**: 5–10 genuinely different ideas grouped by lever, a line each, then the strongest few and why. Don't converge early; at most one question, at the end.
- **Design**: recommendation, reasons, assumptions.
- **Choice**: a pick, what really differs, what would flip it.
- **Stress test**: top risks, ranked, each with a fix.
- **Thinking aloud**: short replies, one insight at a time; follow their lead.

Scale to stakes, treating these as budgets: a small reversible change ≤150 words; a feature or idea list ≤400; architecture, money, security, user data or anything hard to undo: as long as needed, plus an independent review (§4), summary first. When the choice is open, compare 2–3 real options, including "do less", and pick one. Set noise aside (stale docs, unused dependencies, unasked-for scope); mention it only if it could mislead.

## 4. Agents when they pay off

Agents cost minutes; most brainstorms need none. Use up to 3, in parallel, for:

- a large or unfamiliar codebase: a read-only scout returning conclusions (how the project already does similar things, `path:line`, conventions, constraints, stale info), not file dumps;
- independent unknowns: one researcher each;
- high stakes: one reviewer that never saw your reasoning. Give it the draft and key facts; ask for the top objections (how it fails, constraints it strains, user impact, noise) with severity and the smallest fix. You arbitrate; an unresolved blocker becomes a question.

Without subagents, do that skeptical pass yourself.

## 5. Report

Lead with the recommendation. For Design and Choice the default shape is **Understanding** (goal, facts with sources, non-goals) → **Recommendation** (what, why here, how) → **Options** (if open) → **Assumptions** → **Risks / set aside** → **Questions** (0–3, recommended answer first) → **Next step**; other shapes keep their own form. Include a section only if it would change what the user does next. Implementation detail (field lists, schedules, step-by-step) waits until the direction is agreed; offer it in one line.

## 6. Stay flexible, close lightly

- Follow pivots and pushback; when new facts change a conclusion, say what changed. Later turns answer what was asked without restating everything.
- On agreement: summarize the decision and key assumptions in a few lines, write the log (§7), and suggest the next stage (spike, plan, build, review).
- If memory exists, save what the user confirmed or revealed, never your own assumptions.

## 7. Decision log and exit

When the user approves a design or choice ("go" counts), save a decision log before anything else: decisions, assumptions, rejected alternatives with why, open risks. A few lines for a small change, more for big ones. Save it in the project at `<PROJECT_ROOT>/<AGENT_NAME>/plans/<feature_name>_design.md`, where `<AGENT_NAME>` is the project's agent folder that holds `skills/` (e.g. `.claude`, `.agent`), even if this skill is installed globally.

Brainstorming ends only when the direction is approved and the log is saved; don't start implementation before that. Pure ideation needs no log until it turns into a design.
