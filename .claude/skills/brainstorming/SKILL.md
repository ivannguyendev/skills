---
name: brainstorming
description: "Accelerated, autonomous brainstorming that leverages context to propose validated designs quickly, minimizing user interrogation."
risk: low
source: community
date_added: "2026-10-01"
---

# Autonomous Brainstorming & Design

## Purpose

Transform ideas into validated designs rapidly. Act as a proactive, professional assistant: leverage existing context, make smart assumptions, and propose concrete solutions rather than interrogating the user with small, continuous questions.

## Core Directives (Non-Negotiable)

1. **Context First:** Silently read project files, memory, and past decisions. Proactively analyze the context. If context answers a question, DO NOT ask it.
2. **Autonomy & Proactivity:** Do not analyze vaguely or ask continuous small questions. Make reasonable assumptions based on context. If there is enough information, propose a complete design immediately.
3. **Internal Review:** Before presenting, internally simulate a "Skeptic" and "Constraint Guardian" to self-correct edge cases, noise, and scalability limits.
4. **Workflow Synergy:** If the task aligns with a standard process (e.g., SaaS MVP, Security Audit), implicitly structure your proposal to match those workflow stages.

## The Output Format

When brainstorming, your response MUST follow this exact structure to maximize clarity, transparency, and autonomy:

### 1. Context Analysis & Executive Summary

Briefly and transparently state what you understand from the context, the core goal, and any key non-goals. The user must clearly see that you understand the big picture.

### 2. Recommended Design (The Proposal)

If there is sufficient context, propose a complete design immediately. Lead with the BEST option. Include:

- High-level architecture or component flow.
- Trade-offs (Complexity vs. Scalability).
- (Optional) A brief Mermaid diagram if it clarifies the flow.

### 3. Explicit Assumptions

List the default constraints and design decisions you assumed (e.g., Performance, Scale, Security, UI/UX). You must take a stance so the user only has to override if they disagree.

### 4. Pivotal Questions (Only if blocked)

Only ask questions if they are absolute blockers that cannot be safely assumed.
**Crucial:** If you must ask a pivotal question, you MUST transparently explain _why_ you are asking it and how it impacts the design, providing clear context so the user understands before deciding. Provide multiple-choice options or recommendations for each.

### 5. Next Steps

Suggest the immediate next action (e.g., "Approve this proposal to begin implementation" or "Provide feedback on the assumptions").

## Decision Logging (Mandatory)

Upon approval of the design, you MUST log all major decisions, explicit assumptions, and rejected alternatives to a durable markdown file.

- **Location:** Save the decision log in the `plans` directory at the same directory level as the `skills` folder (e.g., if skills is in `<PROJECT_ROOT>/skills`, save to `<PROJECT_ROOT>/<AGENT_NAME>/plans/<feature_name>_design.md`).

## Exit Criteria

The brainstorming phase is complete when the user approves the "Recommended Design" AND the Decision Log has been written to the `plans` directory. Do not proceed to implementation until the log is saved.
