# Release process

A split-history, source-only model (the same one `terva-tasks` uses):

- **`main`** — day-to-day development (can use a `replace terva.sh/terva => ../terva`
  if you co-develop the SDK).
- **`release` / `release/*`** — curated, hand-written history; the **only** refs
  that reach the public mirror.

Maintainer plumbing is in [`../release.just`](../release.just) (imported by the
root justfile via `import?`, so the public tree works without it).

## Source-only: pin the SDK

Published source must build for anyone, so the released `go.mod` must **pin**
`terva.sh/terva` to a real version and carry **no `replace`**. `just
release-overlay vX.Y.Z` does the swap (`go mod edit -dropreplace … -require …` +
`go mod tidy`). The default `go.mod` here already pins a release, so this is a
no-op unless you added a dev `replace`.

## Cut a release

1. One-time: `just mirror-init` (set `EXT_MIRROR_DIR` or the URL in `release.just`).
2. Cut a curated `release` branch from `main` (consider the `/release-cut` skill
   if you have it).
3. `just release-overlay vX.Y.Z` if you used a dev replace; commit it.
4. `just release-check` — scans for a lingering `replace` and your private markers.
5. Push `release` to the mirror (or `just mirror-push`); tag `vX.Y.Z`.

## Lessons (learned the hard way)

- **Once `main` is public, never `git commit --amend` a release.** Amending
  diverges from the published commit, so the next push is a non-fast-forward and
  you're forced to either force-push (rewriting public history) or untangle it.
  **Re-cut each release as a *child* of the published HEAD** instead:

  ```bash
  git fetch origin
  git reset --soft origin/main   # move branch to published tip, keep your tree staged
  git commit                     # new release commit, parented on the last one
  git push origin main           # fast-forward
  ```

- **A `release` tag is cheaper to move than branch history.** If only the tag is
  wrong, `git push --force origin vX.Y.Z` is far less disruptive than rewriting a
  branch.

- **Verify the pinned version resolves AND has the SDK you use** before tagging:

  ```bash
  cd $(mktemp -d) && go mod init probe \
    && go mod edit -require=terva.sh/terva@vX.Y.Z \
    && printf 'package main\nimport "terva.sh/terva/packages/agent/ext"\nfunc main(){_=ext.New("p","0")}\n' > main.go \
    && go mod tidy && go build .
  ```
