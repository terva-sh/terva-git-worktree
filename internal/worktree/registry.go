package worktree

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Registry is this extension's per-repo truth, augmenting git's worktree list
// with what git doesn't track: base commit, claim, and creation time. It lives
// under the extension's data dir, never inside the user's repo.
type Registry struct {
	Worktrees map[string]*Entry `json:"worktrees"`
}

// Entry is the registry record for one managed worktree (keyed by name).
type Entry struct {
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	BaseRef    string `json:"base_ref"`
	CreatedAt  string `json:"created_at"` // RFC3339
	Claim      *Claim `json:"claim"`      // null when available
}

// Claim records who holds a worktree. session_id is the primary liveness signal;
// pid (the extension process) is a coarse backstop since it's shared across a
// terva instance.
type Claim struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	ClaimedAt string `json:"claimed_at"` // RFC3339
}

// loadRegistry reads registry.json through the data layer (writable dir over the
// install dir), returning an empty registry when absent or blank. A parse error
// is returned so callers don't silently clobber state.
func loadRegistry(fs RegistryFS, name string) (*Registry, error) {
	b, err := fs.ReadFile(name)
	if err != nil {
		if os.IsNotExist(err) {
			return &Registry{Worktrees: map[string]*Entry{}}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return &Registry{Worktrees: map[string]*Entry{}}, nil
	}
	var r Registry
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("parse registry %s: %w", name, err)
	}
	if r.Worktrees == nil {
		r.Worktrees = map[string]*Entry{}
	}
	return &r, nil
}

// saveRegistry writes registry.json atomically (temp + rename) so a concurrent
// reader never sees a half-written file.
func saveRegistry(path string, r *Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fileLock is an advisory cross-process lock around a repo-key's registry. Two
// terva instances can target the same repo, so every op takes it. flock is
// released automatically if the holding process dies, so it can't deadlock.
type fileLock struct{ f *os.File }

func acquireLock(path string) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// pidAlive reports whether pid names a live process. signal 0 probes existence:
// nil => alive, EPERM => alive but not ours, ESRCH => gone.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
