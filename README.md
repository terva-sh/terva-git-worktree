# terva-git-worktree

A [terva](https://github.com/terva-sh/terva) extension that lets the **agent**
create, list, and remove git worktrees for the current repository through tools,
and reason about reuse: every worktree reports whether it is **`available`** or
**`claimed`** and the **commit it was branched from**, so the agent can decide
whether it already has a suitable worktree or needs a new one.

This is **phase 1** — the three agent tools. The human-facing `/worktree` panel +
`/cd`-to-switch UX and the swarm "one worktree per subagent" integration are
later phases (see `terva-git-worktree-design.md`).

## Tools

| Tool | Effect | Summary |
|------|--------|---------|
| `worktree_list` | read-only | Lists worktrees with status (`available`/`claimed`), `base_commit`/`base_ref`, `head_commit`, `dirty`, `claimed_by`, `stale_reason`, plus `repo_key` and `cwd_worktree` (the one you're in, or null). The agent's pre-decision call; declared `ReadOnly()` so it runs promptless. |
| `worktree_create` | side-effecting | Creates (or, when the name already exists and is available, **reuses**) a worktree and **claims** it for this session. Optional `base` ref/SHA (default current HEAD); `reuse_if_available` (default true). Branch is `wt/<name>`. |
| `worktree_remove` | side-effecting | Removes a managed worktree. **Refuses** on uncommitted or unmerged/unpushed work unless `force: true`. Leaves the branch unless `delete_branch: true`. |

All tools return JSON as their text result so the agent can parse and reason over
the available/claimed model and base-commit drift.

### The model: `available` / `claimed`

Git is the source of truth for *existence* (`git worktree list --porcelain`); the
extension's `registry.json` augments each worktree with **base commit**, **claim**
(session id + pid + time), and **created_at**.

- **`claimed`** — a live claim: the claim's `session_id` is the current session
  (`claimed_by: "self"`), or another session whose pid is alive and within TTL
  (`claimed_by: "<session>"`).
- **`available`** — no claim, or a **stale** one (owning pid gone, session
  ended, or past the TTL). Stale claims are reported with `stale_reason` and are
  reclaimable, never silently stolen.

`worktree_create` claims for the current session; asking for an existing
*available* name claims and returns it (`reused: true`) instead of erroring. A
name claimed by another *live* session is refused. Claims are released by
`worktree_remove` or go stale when the owning session/pid is gone.

## Storage & repo keying

Worktree checkouts and metadata live under the extension's **own** data dir,
never inside the user's repo:

```
$TERVA_HOME/ext-data/terva-git-worktree/
  <repo-key>/
    registry.json          # claim + base-commit metadata
    registry.lock          # advisory cross-process lock (flock)
    worktrees/<name>/       # the actual `git worktree add` checkout
```

The `<repo-key>` is derived from the git **common dir**
(`git rev-parse --git-common-dir`), not cwd or the host `ProjectID` (both
cwd-keyed, which would scatter a worktree's view from the main checkout's), so it
is **stable across the main checkout and every worktree of it** — an instance
running inside worktree-A sees the same worktrees as one in the main checkout.
The registry is read through `Host().DataFS()` (the writable data dir layered
over the install dir, so it survives a future data-dir move) and written
atomically (temp + rename) under a lockfile, since two terva instances can target
the same repo. The worktree checkouts are plain dirs under the data dir (git owns
their contents).

## Permissions

`worktree_list` is declared `ReadOnly()`, so the host admits it without a prompt
in read-only/workspace approval modes — the agent's pre-decision call is cheap
and promptless.

`worktree_create` and `worktree_remove` are side-effecting, so they **ask** in
`workspace` mode (the interactive default) — which is the intended gate for the
one irreversible op, removing a checkout. For enforcement across all modes
(headless/yolo), add a permission rule pinning `worktree_remove` to `ask`/`deny`.

## Requirements

terva **v0.105.2+** — extension protocol v2 plus the SDK pieces this extension
relies on: the `ReadOnly()` tool option, `Host().DataFS()`, and `Host().CWD`
following `/cd` (it rides `session_start`). Go **1.22+**, and a `git` binary on
PATH (the extension shells out with explicit `-C`).

## Layout

```
main.go               registration: tools, descriptions, schemas, contextPolicy
app.go                thin SDK glue: parse args → worktree.Manager → JSON result
internal/worktree/    the feature — pure, unit-tested without the SDK:
  repo.go             repo resolution (git-common-dir keying), paths, slug
  git.go              git shell-out + porcelain parsing + dirty/unmerged checks
  registry.go         registry.json (atomic save), flock, pid liveness
  worktree.go         Manager: status derivation + Create/List/Remove
  worktree_test.go    table-driven tests against a real temp git repo
internal/extutil/     reusable helpers (input sanitization, safe filenames)
run.sh                launcher: builds on first launch, execs the cached binary
justfile              test / lint / fmt / build / install / try / ci / clean
release.just          maintainer-only: mirror + release-overlay + leak check
docs/                 release-process.md, extension-patterns.md
```

## Develop

```bash
just test     # go test -race ./internal/...   (needs git, not terva)
just lint     # go vet + gofmt check
just build    # build ./terva-git-worktree
just install  # build + (re)install into terva for dogfooding
just try DIR  # build + launch terva with the extension (cwd = DIR)
just ci       # lint + race tests
```

To build against a **local terva checkout** (e.g. for an unreleased SDK
feature):

```bash
go mod edit -replace terva.sh/terva=../terva    # develop against ../terva
go mod edit -dropreplace terva.sh/terva         # back to the pinned release
```

## Install

```bash
terva ext install https://github.com/terva-sh/terva-git-worktree.git
```

terva clones the repo and `run.sh` builds it on first launch (Go 1.22+; no
committed binary). You can also install from a local clone
(`terva ext install .`). For local development use `just install` (it builds and
copies the binary in, since `terva ext install` skips git-ignored files).

## Release

Source-only via a curated `release` branch and a public mirror — see
[docs/release-process.md](docs/release-process.md). Short version: cut a release
branch, `just release-overlay vX.Y.Z` (pin the SDK), `just release-check`, push.

## License

MIT — see [LICENSE](LICENSE).
