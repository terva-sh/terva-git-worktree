#!/usr/bin/env bash
# terva-git-worktree launcher.
#
# terva runs an extension by executing the manifest's `exec` verbatim — it never
# compiles Go. This wrapper builds the binary on first launch (and after any
# source change), then execs it, so `terva ext install <path|git-url>` works on
# any host with a Go toolchain, without committing a platform-specific binary.
#
# IMPORTANT: stdout is the protocol wire. Every byte of build chatter must go to
# stderr (terva captures it to $TERVA_HOME/logs/ext-terva-git-worktree.log); a
# stray stdout write corrupts the JSON stream.
set -euo pipefail
cd "$(dirname "$0")"

bin="./terva-git-worktree"

needs_build() {
	[ -x "$bin" ] || return 0
	if [ -n "$(find . -name '*.go' -newer "$bin" -print -quit 2>/dev/null)" ]; then
		return 0
	fi
	if [ go.mod -nt "$bin" ]; then
		return 0
	fi
	if [ -f go.sum ] && [ go.sum -nt "$bin" ]; then
		return 0
	fi
	return 1
}

if needs_build; then
	if ! command -v go >/dev/null 2>&1; then
		echo "[terva-git-worktree] Go toolchain not found on PATH; cannot build." >&2
		echo "[terva-git-worktree] Install Go 1.22+ (https://go.dev/dl/) and relaunch terva." >&2
		exit 1
	fi
	echo "[terva-git-worktree] building $bin (first launch or sources changed)…" >&2
	go build -o "$bin" . >&2
	echo "[terva-git-worktree] build complete." >&2
fi

exec "$bin" "$@"
