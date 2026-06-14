# terva extension patterns

The reasoning behind this template, distilled from building `terva-tasks`. Read
this before you delete things.

## Structure: pure logic + thin glue

Split the code so the domain logic is unit-testable **without** starting the
extension:

- `internal/<feature>/` — the model, persistence, rendering, invariants. Stdlib
  only (plus `extutil`). This is where the tests live and where the real work is.
- `app.go` — thin SDK glue: parse tool args → call the store → push surfaces →
  return an `ext.ToolResult`. Validated end-to-end with a manual wire test or the
  SDK's `io.Pipe` harness, but it should hold almost no logic.
- `main.go` — registration only.

`just test` runs `./internal/...` and needs neither terva nor a network.

## The launcher (run.sh)

terva runs an extension by executing the manifest's `exec` verbatim; it never
compiles Go. `run.sh` bridges that: build the binary on first launch (and after
source changes), then `exec` it. This makes `terva ext install <path|git-url>`
work anywhere with a Go toolchain, with **no committed binary**.

- **stdout is the wire.** All build chatter must go to **stderr** or you corrupt
  the JSON protocol stream.
- `just install` copies the freshly-built binary into the install dir, because
  `terva ext install` is git-aware and skips the (git-ignored) binary.

## Dependency: pin for release, replace for dev

`go.mod` **pins** a released `terva.sh/terva` by default, so the template builds
for anyone. To develop against a local terva checkout (unreleased SDK features),
add a `replace` and drop it before releasing (`just release-overlay` does both).
A binary doesn't care about `go.mod` at runtime, but `run.sh`/`go run` do — so an
installed copy must resolve the dependency, which the pin (not a local replace)
guarantees.

## RequireProtocol — fail cleanly on old hosts

`e.RequireProtocol(2)` declares a floor. A host that's too old (upstream zot or a
pre-v2 terva) refuses to load the extension with a clear message instead of
running it against a wire it half-speaks. Use it whenever you depend on a
protocol-version feature (session identity, context cards — both protocol 2).

## Session identity → per-session state

`session_start` carries `session_id` / `session_path` / `session_title`, fires
**after** the session opens and **again on every change** (resume / fork / `/new`),
and (as of terva v0.105.1) is delivered in **headless modes too** (`-p`, `--json`,
swarm). RPC is sessionless (empty id).

```go
e.OnSession(func(s ext.Session) { store.Rebind(s.ID) }) // "" => no session
```

Key your file `<DataDir>/<prefix>-<id>.json`. The store's `Rebind` is **transactional**
(load into locals, commit on a clean read) so a corrupt file can't leak the prior
session's data; a corrupt file is moved aside and the session starts empty. On an
empty id, stay **in memory** — never write `<prefix>-.json`.

## Context surface (the big one)

The host owns wrapping, bounds, ordering, cadence, and cache placement; you supply
content. Gate on `RequireProtocol(2)`.

- `ContributeContext(text)` — **static** standing policy, folded into the *cached*
  system prompt. Put your when/when-not and invariants here. Then **shrink the
  tool descriptions** — but keep a minimal restatement in them, because a user or
  project can opt out with `disable_context_extensions` (which suppresses your
  context but keeps your tools).
- `PushContextCard(Card{...})` — **live** state injected each turn at the
  cache-free tail (never in the transcript, so compaction can't eat it, and it
  survives `/clear`). Call it on every mutation. Keep it compact — it costs tokens
  every turn — and bounded (host cap: ~4 KiB/card). `ClearContextCard` when empty.
- `Card{Blocking:true}` while "open work" remains — the host nudges the model to
  review before declaring the turn done. (Exclude "intentionally parked" states,
  or you'll nag honest stops.)
- `SetStatus(id, text)` — a short TUI status segment (not model-facing); empty
  clears it.

This is what makes an extension feel native: the model sees your state and policy
without calling a tool, and recovers after `/clear` automatically.

## Tools

Tool **descriptions** are model-facing — carry the essential usage policy there
(the full version lives in `ContributeContext`). Generate IDs yourself; never
accept a model-supplied ID. Enforce invariants (e.g. "exactly one active") in the
store and **state them in the result text** so the model sees what changed.

## Hardening (cheap, do it up front)

- **Sanitize every model/host/user display field** with `extutil.CleanOneLine` —
  newlines/ANSI/control chars otherwise inject fake lines into tool output and the
  panel. Apply at ingress (so stored data is clean) and defensively at render.
- **Traversal-safe session filenames** (`extutil.SessionFileName`) — never build a
  path from a raw wire string.
- **Caps** on field length, batch size, and item count — bound persisted state and
  per-turn context.
- **Oversized inbound frames**: the host caps a tool-call's args (~1 MiB) and
  returns a normal `is_error` rather than a frame that would kill you; per-frame
  max is 4 MiB both directions and oversized frames are skipped, not fatal. You
  still bound your own output.
- **Read-only tools**: pass `ext.ReadOnly()` to `ext.Tool` (terva v0.105.2+) to
  declare a tool side-effect free, so the host admits it without a prompt in
  read-only/workspace approval modes.

## Panel

`OpenPanel` / `RenderPanel` / `ClosePanel` + `OnPanelKey` (keys: up/down/enter/esc/
backspace/`rune`). It's optional UI; the model never sees it. Update it from the
same `refresh()` that pushes the context card, but only when it's open.

## Release

See [release-process.md](release-process.md) — the key trap: once `main` is
public, re-cut each release as a *child* of the published HEAD; don't `--amend`.
