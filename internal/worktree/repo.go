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
}

// repo identifies the canonical git repository shared across the main checkout
// and all of its worktrees. The key is derived from the git *common* dir, so it
// is stable whether cwd is the main repo or any worktree of it.
type repo struct {
	fs  RegistryFS
	cwd string // the caller's cwd, used for repo-level `git -C` calls
	key string // stable per-repo storage key (<DataDir>/<key>/...)
}

// resolveRepo derives the canonical repo identity from env.CWD. It keys on the
// git *common* dir (shared by the main checkout and every linked worktree)
// rather than cwd or the host ProjectID (both cwd-keyed, which would scatter a
// worktree's view from the main checkout's) — exactly what list/reuse needs.
func resolveRepo(env Env) (*repo, error) {
	if env.CWD == "" {
		return nil, fmt.Errorf("no working directory reported by host")
	}
	if env.FS == nil {
		return nil, fmt.Errorf("no data directory reported by host")
	}
	common, err := runGit(env.CWD, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("not a git repository (cwd %s): %w", env.CWD, err)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(env.CWD, common)
	}
	common = canonPath(common)
	return &repo{fs: env.FS, cwd: env.CWD, key: repoKey(common)}, nil
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
