package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxNameLen bounds a slugged worktree name so it stays a sane branch/dir name.
const maxNameLen = 64

// RegistryFS is the minimal data-layer this package needs: a read-through reader
// (writable DataDir over the read-only install dir) plus a resolver for the real
// writable path of a name. The terva SDK's ext.DataFS satisfies it exactly, so
// app.go passes Host().DataFS() straight in; tests pass a plain-dir
// implementation. Keeping it an interface keeps this package SDK-free.
type RegistryFS interface {
	// ReadFile reads name from the writable layer, falling back to the
	// read-only install layer — so a registry written before the data dir
	// moved stays readable until it is rewritten.
	ReadFile(name string) ([]byte, error)
	// Path returns the real writable path where name lives (DataDir/name)
	// without creating anything. The worktree checkouts, the lockfile, and the
	// atomic registry write all need real on-disk paths.
	Path(name string) (string, error)
}

// Env is the per-call host environment: the data layer, the cwd to resolve the
// canonical repo from, and the session that owns new claims. The fields come
// from Host().DataFS() / Host().CWD (which now follows /cd) / Host().SessionID.
type Env struct {
	FS        RegistryFS // extension data layer (DataDir over install dir)
	CWD       string     // resolve the repo from here (never the extension's own cwd)
	SessionID string     // claim owner identity; "" => no active session
	// RepoRoot is an optional explicit target repo (the tool's repo_root arg),
	// resolved instead of CWD. Absolute, or relative to CWD. It is an escape
	// hatch for operating on a repo the cwd is not inside; CWD stays the
	// authority for "which worktree am I in" (cwd_worktree). "" => use CWD.
	RepoRoot string
}

// repo identifies the canonical git repository shared across the main checkout
// and all of its worktrees. The key is derived from the git *common* dir, so it
// is stable whether cwd is the main repo or any worktree of it.
type repo struct {
	fs  RegistryFS
	cwd string // the caller's cwd, used for repo-level `git -C` calls
	key string // stable per-repo storage key (<DataDir>/<key>/...)
}

// resolveRepo derives the canonical repo identity from env.CWD — or from
// env.RepoRoot when the caller supplies that override. It keys on the git
// *common* dir (shared by the main checkout and every linked worktree) rather
// than cwd or the host ProjectID (both cwd-keyed, which would scatter a
// worktree's view from the main checkout's) — exactly what list/reuse needs.
func resolveRepo(env Env) (*repo, error) {
	if env.CWD == "" {
		return nil, fmt.Errorf("no working directory reported by host")
	}
	if env.FS == nil {
		return nil, fmt.Errorf("no data directory reported by host")
	}
	// dir is both where we probe for the repo and the -C directory for every
	// repo-level git call (repo.cwd). It defaults to the host cwd — left
	// untouched so behavior is byte-for-byte identical without an override — and
	// becomes the resolved RepoRoot when one is given.
	dir := env.CWD
	if env.RepoRoot != "" {
		dir = env.RepoRoot
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(env.CWD, dir)
		}
		dir = canonPath(dir)
	}
	common, err := runGit(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		if env.RepoRoot != "" {
			return nil, fmt.Errorf("not a git repository (repo_root %s): %w", env.RepoRoot, err)
		}
		return nil, noRepoError(env.CWD, err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	common = canonPath(common)
	return &repo{fs: env.FS, cwd: dir, key: repoKey(common)}, nil
}

const (
	// maxRepoProbe bounds how many .git-bearing children we confirm with a git
	// invocation, so a directory full of .git-looking entries can't fan out into
	// many git calls. The pre-filter (a .git entry must exist) keeps this small.
	maxRepoProbe = 8
	// maxRepoHints bounds how many discovered repos we name in an error, so a
	// scratch dir holding many checkouts doesn't dump an unbounded list.
	maxRepoHints = 3
)

// nearbyRepos does a shallow, bounded scan of cwd's immediate children for git
// checkouts, returning their cwd-relative paths (e.g. "./terva") — the input to
// a "you're one directory away" hint. Best-effort: an unreadable cwd or a child
// that doesn't confirm yields no entry, never a failure. It never recurses and
// never follows symlinks. Bare repos (no .git entry) are naturally excluded —
// they're a poor /cd target anyway.
func nearbyRepos(cwd string) []string {
	entries, err := os.ReadDir(cwd) // sorted by name => deterministic output
	if err != nil {
		return nil
	}
	var found []string
	probed := 0
	for _, e := range entries {
		// Immediate child directories only; skip files and symlinks (a symlink
		// reports !IsDir here), so a symlink loop or a symlink to a huge tree
		// cannot blow up the scan.
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(cwd, e.Name())
		// A .git entry — a dir for a normal checkout, a file for a linked
		// worktree — is the cheap pre-filter before we spend a git invocation.
		// Lstat so a symlinked .git doesn't get followed.
		if _, err := os.Lstat(filepath.Join(child, ".git")); err != nil {
			continue
		}
		if probed >= maxRepoProbe {
			break
		}
		probed++
		// Confirm it's a real checkout with the exact probe resolveRepo trusts,
		// not a stray .git that isn't a repo.
		if _, err := runGit(child, "rev-parse", "--git-common-dir"); err != nil {
			continue
		}
		found = append(found, "./"+e.Name())
	}
	return found
}

// noRepoError builds the error resolveRepo returns when cwd is not a git repo.
// When the shallow scan finds checkouts one directory down it folds concrete
// next steps into the message (the discoverability win); with nothing nearby it
// returns today's bare error unchanged (cause wrapped), so the no-repo-nearby
// case doesn't regress into noisier output.
func noRepoError(cwd string, cause error) error {
	near := nearbyRepos(cwd)
	if len(near) == 0 {
		return fmt.Errorf("not a git repository (cwd %s): %w", cwd, cause)
	}
	if len(near) == 1 {
		r := near[0]
		return fmt.Errorf("not a git repository (cwd %s); found a git repo at %s — cd there (/cd %s) or pass repo_root:%q to operate on it from here",
			cwd, r, r, r)
	}
	shown := near
	extra := 0
	if len(shown) > maxRepoHints {
		extra = len(shown) - maxRepoHints
		shown = shown[:maxRepoHints]
	}
	list := strings.Join(shown, ", ")
	if extra > 0 {
		list = fmt.Sprintf("%s (and %d more)", list, extra)
	}
	return fmt.Errorf("not a git repository (cwd %s); found git repos nearby: %s — cd into one (e.g. /cd %s) or pass repo_root (e.g. repo_root:%q)",
		cwd, list, near[0], near[0])
}

// dataPath resolves <DataDir>/<key>/<rel> to a real writable filesystem path.
func (r *repo) dataPath(rel string) string {
	name := r.key
	if rel != "" {
		name = r.key + "/" + rel
	}
	p, err := r.fs.Path(name)
	if err != nil {
		// Names are a slug+hash key plus slugged sub-paths, so this can't escape
		// the layer; fall back to the relative name defensively.
		return name
	}
	return p
}

func (r *repo) worktreesDir() string            { return r.dataPath("worktrees") }
func (r *repo) worktreePath(name string) string { return r.dataPath("worktrees/" + name) }
func (r *repo) registryPath() string            { return r.dataPath("registry.json") }
func (r *repo) registryName() string            { return r.key + "/registry.json" }
func (r *repo) lockPath() string                { return r.dataPath("registry.lock") }

// repoKey mirrors core.ProjectKey: a readable prefix (the repo's directory name)
// plus a collision-proof suffix (a short hash of the absolute common dir).
func repoKey(commonDir string) string {
	prefix := slugify(filepath.Base(filepath.Dir(commonDir)))
	if prefix == "" {
		prefix = "repo"
	}
	sum := sha256.Sum256([]byte(commonDir))
	return prefix + "-" + hex.EncodeToString(sum[:5])
}

// slugify lowercases and reduces a string to [a-z0-9-], collapsing separators to
// a single dash and trimming dashes from the ends. It is used for both repo-key
// prefixes and worktree names (which become branch wt/<name>).
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '/' || r == '.' || r == ':':
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > maxNameLen {
		out = strings.Trim(out[:maxNameLen], "-")
	}
	return out
}

// dirExists reports whether p exists on disk (file or directory).
func dirExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// canonPath cleans a path and resolves symlinks when it exists, so paths from
// git (which may be symlink-resolved, e.g. macOS /var → /private/var) compare
// equal to paths we construct ourselves. Falls back to a plain Clean when the
// path doesn't exist yet.
func canonPath(p string) string {
	p = filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
