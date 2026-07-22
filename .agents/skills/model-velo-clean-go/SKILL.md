---
name: model-velo-clean-go
description: Keep Model-Velo Go changes concise, domain-shaped, and consistent with the repository. Use when writing, reviewing, simplifying, or refactoring Go files in this project, especially under internal/httpapi, internal/reliability, internal/provider, internal/config, and cmd.
---

# Model-Velo Clean Go

Write the smallest clear change that preserves Model-Velo's real safety boundaries. Optimize for the next maintainer, not for an AI-authorship score.

## Respect project authority

1. Follow the repository `AGENTS.md` and the user's requested behavior first.
2. Preserve protocol, authentication, concurrency, timeout, cancellation, persistence, and secret-handling guarantees.
3. Treat fewer lines as a useful signal, never as the objective. Do not delete a necessary boundary check merely to make code look less generated.

## Work from the codebase

1. Read the target file, its direct callers, and the nearest related types before proposing a change.
2. State the goal, non-goal, acceptance condition, touched files, and call chain for a behavioral slice.
3. Match existing package ownership, error vocabulary, naming, and comment density.
4. Separate behavior changes from behavior-preserving cleanup. Make the distinction explicit in review findings.
5. Keep the diff limited to the requested slice; leave unrelated cleanup alone.

## Keep the design earned

- Put HTTP parsing and response mapping in `internal/httpapi`; keep routing, Retry, Fallback, and SQL out of handlers.
- Put ordered candidate planning in `internal/routing`, reliability policy and state in `internal/reliability`, and upstream protocol work in `internal/provider`.
- Create an interface, factory, registry, or options type only for a current second implementation, consumer boundary, or testable dependency seam.
- Extract a helper only when its name compresses domain meaning, it removes real duplication, or it isolates a resource-lifecycle boundary.
- Keep one owner for each policy and mutable state. Do not repeat Retry/Fallback decisions in Handler, Adapter, and Orchestrator.
- Prefer Go's standard library and existing dependencies. Do not add a dependency for a small local operation.
- Allow small, stable duplication when the abstraction would hide meaning or couple unrelated paths.

## Preserve reliability boundaries

- Validate untrusted environment, HTTP, provider, and persistence input at the boundary; trust validated internal values afterward.
- Propagate `context.Context` through Queue waits, backoff, upstream calls, and Fallback. Never replace cancellation with polling or sleep.
- Pair acquired resources with immediate cleanup: cancel functions, timers, response bodies, Queue leases, and Breaker permits.
- Retry only through the shared failure policy. Never broaden retryability because an error looks temporary.
- Keep total request budget distinct from per-attempt timeout.
- Never swallow an error or replace it with plausible fake output.
- Never log or return API Key secrets, authorization headers, prompts, or raw upstream error bodies.

## Make Go read naturally

- Prefer guard clauses and a flat happy path.
- Use `:=` for derived non-zero values and `var` when the useful initial state is the zero value.
- Keep variables close to their use. Retain an intermediate variable when its name explains a calculation or state transition.
- Use short domain names instead of generic names such as `resultData`, `helper`, `manager`, or `processor`.
- Break long calls and signatures at semantic boundaries. Do not create an options struct solely to satisfy a parameter-count rule.
- Comment business reasons, invariants, compatibility constraints, and non-obvious ownership. Delete comments that narrate the next line or the prompt history.
- Avoid step-marker comments, generated-code banners, speculative TODOs, and decorative section dividers.
- Prefer direct control flow over clever expressions. A reader should be able to explain every retry, state change, and cleanup path.

## Review without theatrics

Prioritize findings in this order:

1. Correctness, cancellation, resource leaks, security, and concurrency.
2. Wrong package ownership, duplicated policy, premature abstraction, and behavior hidden by generic names.
3. Redundant branches, filler variables, tautological comments, and formatting not already handled by `gofmt`.

Report only concrete findings with a file and line. Do not invent an "AI percentage" or rewrite correct code merely to make it stylistically irregular. Say plainly when the file is already clear.

## Verify once per slice

1. Run `gofmt` on changed Go files.
2. Run the narrowest relevant test while iterating only when needed.
3. Before delivery, run `go test ./...` and `go vet ./...` once.
4. Add tests only for a new high-risk boundary or previously unproved behavior, following the limits in `AGENTS.md`.
5. If race or external infrastructure is unavailable, record the gap instead of manufacturing substitute evidence.

Summarize the behavior, the reason for the shape of the code, verification performed, and any remaining boundary. Keep the handoff short.
