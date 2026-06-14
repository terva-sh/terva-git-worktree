set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# Maintainer-only release plumbing (mirror + curated release branches). Optional
# import: the public tree ships without release.just and this justfile still works.
import? 'release.just'

# List recipes.
default:
    @just --list

# Unit-test the pure logic (no SDK / no terva needed).
test *ARGS:
    go test -race ./internal/... {{ARGS}}

# Vet + gofmt check.
lint:
    go vet ./...
    @test -z "$(gofmt -l . | tee /dev/stderr)" || { echo "gofmt issues (run \`just fmt\`)"; exit 1; }

# Format all Go sources.
fmt:
    gofmt -w .

# Build the launcher's binary (./terva-git-worktree, the run.sh exec target).
build:
    go build -trimpath -o terva-git-worktree .
    @echo "built ./terva-git-worktree"

# Build, then (re)install into terva's extensions dir so the latest binary loads.
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    name="terva-git-worktree"
    terva ext remove "$name" -y >/dev/null 2>&1 || true
    terva ext install .
    # Install dir is the last column of `ext list`; the path can contain spaces,
    # so take everything from the first '/'.
    line="$(terva ext list | grep -E "/${name}\$" || true)"
    [[ -n "$line" ]] || { echo "install: could not find $name in 'terva ext list'" >&2; exit 1; }
    dir="/${line#*/}"
    # `ext install` is git-aware and skips .gitignore'd files — including the
    # built binary — so copy it in so the extension can run immediately.
    cp -f terva-git-worktree "$dir/terva-git-worktree"
    echo "installed -> $dir"
    terva ext list

# Build, then load into a one-off terva session for manual testing (DIR = cwd).
try DIR=".": build
    terva --ext . --cwd "{{DIR}}"

# Pre-push gate: lint + race tests.
ci: lint
    go test -race ./internal/...

# Tidy go.mod / go.sum.
tidy:
    go mod tidy

# Remove build output.
clean:
    rm -f terva-git-worktree
    rm -rf bin
