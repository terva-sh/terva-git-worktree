package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollect(t *testing.T) {
	repoDir, dataDir := newRepo(t)
	m := fixedManager(true)

	// "busy": two commits ahead of base, plus an uncommitted change.
	busy, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "busy", ReuseIfAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"first change", "second change"} {
		f := filepath.Join(busy.Path, strings.ReplaceAll(msg, " ", "_")+".txt")
		if err := os.WriteFile(f, []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		runT(t, busy.Path, "add", "-A")
		runT(t, busy.Path, "commit", "-q", "-m", msg)
	}
	if err := os.WriteFile(filepath.Join(busy.Path, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// "idle": fresh, nothing ahead.
	if _, err := m.Create(env(repoDir, dataDir, "s1"), CreateArgs{Name: "idle", ReuseIfAvailable: true}); err != nil {
		t.Fatal(err)
	}

	res, err := m.Collect(env(repoDir, dataDir, "s1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Worktrees) != 2 {
		t.Fatalf("want 2 worktrees, got %d", len(res.Worktrees))
	}
	by := map[string]*CollectItem{}
	for _, it := range res.Worktrees {
		by[it.Name] = it
	}
	if b := by["busy"]; b == nil || b.Ahead != 2 || len(b.Commits) != 2 || !b.Dirty || !b.Unpushed {
		t.Errorf("busy collect: %+v", b)
	}
	// Newest commit first in the oneline list.
	if b := by["busy"]; b != nil && !strings.Contains(b.Commits[0], "second change") {
		t.Errorf("busy commits should be newest-first: %+v", b.Commits)
	}
	if i := by["idle"]; i == nil || i.Ahead != 0 || len(i.Commits) != 0 || i.Dirty || i.Unpushed {
		t.Errorf("idle collect: %+v", i)
	}
}

func TestCollectLines(t *testing.T) {
	// Empty.
	if l := CollectLines(&CollectResult{}); len(l) == 0 || !strings.Contains(l[0], "No worktrees") {
		t.Errorf("empty collect: %v", l)
	}
	// All even → reassuring closing line, no merge hint.
	even := &CollectResult{Worktrees: []*CollectItem{{Name: "a", Branch: "wt/a", BaseRef: "main"}}}
	le := CollectLines(even)
	joinedEven := strings.Join(le, "\n")
	if !strings.Contains(joinedEven, "nothing to collect") || !strings.Contains(joinedEven, "even with their base") {
		t.Errorf("all-even collect should reassure: %v", le)
	}
	// Pending work → header, commit lines, flags, and the manual-merge reminder.
	pend := &CollectResult{Worktrees: []*CollectItem{
		{Name: "feat", Branch: "wt/feat", BaseRef: "main", Ahead: 2, Commits: []string{"abc feat: a", "def feat: b"}, Dirty: true, Unpushed: true},
	}}
	lp := CollectLines(pend)
	joined := strings.Join(lp, "\n")
	for _, want := range []string{"feat", "[wt/feat]", "+2 ahead of main", "✱dirty", "⇡unpushed", "feat: a", "merge manually"} {
		if !strings.Contains(joined, want) {
			t.Errorf("collect view missing %q in:\n%s", want, joined)
		}
	}
}
