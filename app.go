package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"terva-git-worktree/internal/worktree"

	"terva.sh/terva/packages/agent/ext"
)

const panelID = "terva-git-worktree-panel"

// app is the thin SDK glue over the pure worktree.Manager: it parses tool args,
// resolves the host environment (cwd / data dir / session), calls the manager,
// and returns the manager's result as JSON the model can parse. It also drives
// the human-facing /worktree panel. All real logic lives in internal/worktree.
type app struct {
	e   *ext.Extension
	mgr *worktree.Manager

	mu          sync.Mutex
	panelOpen   bool
	mode        string // "" / "list" | "collect"
	selected    int
	last        *worktree.ListResult    // most recent list snapshot (for panel keys)
	lastCollect *worktree.CollectResult // most recent collect snapshot

	ctxDecided bool   // whether the static-context block has been decided yet
	ctxSession string // session id the current static-context decision is pinned to
}

func newApp(e *ext.Extension) *app {
	return &app{e: e, mgr: worktree.NewManager()}
}

// onSession exists so the extension subscribes to session_start; the SDK updates
// Host().SessionID (our claim owner) and Host().CWD before this runs. On a new
// session it pins what the extension shows the model — policy and tools (see
// pinContext). If the panel is open we also refresh it (a /cd may have moved us
// to a different repo); otherwise we skip the git work for sessions that never
// touch worktrees.
func (a *app) onSession(s ext.Session) {
	a.e.Logf("active session: %q", s.ID)
	a.pinContext(s)
	a.mu.Lock()
	open := a.panelOpen
	a.mu.Unlock()
	if open {
		a.refresh()
	}
}

// pinContext decides, once per session, what this extension puts in front of the
// model and pins it for the session's life: when git is available at or near the
// session's cwd it keeps the standing worktree policy and tools; when it isn't,
// it drops the policy and — on protocol 4+ — withdraws all five tools, so a
// workspace with no usable repo gets nothing from this extension to burn tokens
// on or misfire. It acts ONLY on a genuinely new session id, never on a
// same-session /cd: both the context block and the tool set live in the cached
// prompt prefix, so flipping either mid-session would evict the prompt cache.
// Mutations go through the boundary-scoped Session handle (cache-safe by
// construction). Context needs host protocol 3; tool withdrawal needs protocol 4
// — older hosts keep the startup policy and all tools visible.
func (a *app) pinContext(s ext.Session) {
	proto := s.ProtocolVersion()
	if proto < 3 {
		return // no RefreshContext on this host; keep the startup contribution
	}
	a.mu.Lock()
	isNew := !a.ctxDecided || s.ID != a.ctxSession
	a.ctxDecided, a.ctxSession = true, s.ID
	a.mu.Unlock()
	if !isNew || s.ID == "" {
		return // same-session /cd (pinned), or a session close / no-session
	}
	cwd := s.CWD
	if cwd == "" {
		cwd = a.e.Host().CWD
	}
	if worktree.GitAvailable(cwd) {
		s.RefreshContext(contextPolicy) // host no-ops if already the active block
		if proto >= 4 {
			s.RestoreAllTools() // host no-ops if nothing was withdrawn
		}
		a.e.Logf("session %q: git at/near %q — worktree policy kept, tools visible", s.ID, cwd)
	} else {
		s.RefreshContext("") // drop the standing policy
		tools := "kept (host < protocol 4)"
		if proto >= 4 {
			s.WithdrawAllTools() // hide all five — nothing they can do here
			tools = "withdrawn"
		}
		a.e.Logf("session %q: no git at/near %q — worktree policy excluded, tools %s", s.ID, cwd, tools)
	}
}

// env snapshots the live host environment for a call. Host() is kept current by
// the SDK: the session id is updated before handlers run, and CWD now follows a
// /cd (it rides session_start), so reading it per-call resolves the right repo.
// DataFS() is the read-through data layer the registry persists through.
func (a *app) env() (worktree.Env, error) {
	h := a.e.Host()
	if h.CWD == "" {
		return worktree.Env{}, fmt.Errorf("host did not report a working directory")
	}
	if h.DataDir == "" {
		return worktree.Env{}, fmt.Errorf("host did not report a data directory")
	}
	return worktree.Env{FS: h.DataFS(), CWD: h.CWD, SessionID: h.SessionID}, nil
}

type createArgs struct {
	Name             string `json:"name"`
	Base             string `json:"base"`
	ReuseIfAvailable *bool  `json:"reuse_if_available"`
	RepoRoot         string `json:"repo_root"`
}

func (a *app) handleCreate(raw json.RawMessage) ext.ToolResult {
	var in createArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult("invalid args: " + err.Error())
	}
	env, err := a.env()
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	env.RepoRoot = in.RepoRoot
	reuse := true // default true per the design
	if in.ReuseIfAvailable != nil {
		reuse = *in.ReuseIfAvailable
	}
	res, err := a.mgr.Create(env, worktree.CreateArgs{
		Name: in.Name, Base: in.Base, ReuseIfAvailable: reuse,
	})
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	a.refresh()
	return jsonResult(res)
}

type listArgs struct {
	Match *struct {
		Status  string `json:"status"`
		BaseRef string `json:"base_ref"`
		Mine    bool   `json:"mine"`
	} `json:"match"`
	RepoRoot string `json:"repo_root"`
}

func (a *app) handleList(raw json.RawMessage) ext.ToolResult {
	var in listArgs
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &in) // args are optional
	}
	env, err := a.env()
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	env.RepoRoot = in.RepoRoot
	var filter worktree.ListFilter
	if in.Match != nil {
		filter = worktree.ListFilter{Status: in.Match.Status, BaseRef: in.Match.BaseRef, Mine: in.Match.Mine}
	}
	res, err := a.mgr.List(env, filter)
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	return jsonResult(res)
}

type nameArgs struct {
	Name     string `json:"name"`
	RepoRoot string `json:"repo_root"`
}

func (a *app) handleClaim(raw json.RawMessage) ext.ToolResult {
	var in nameArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult("invalid args: " + err.Error())
	}
	env, err := a.env()
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	env.RepoRoot = in.RepoRoot
	res, err := a.mgr.Claim(env, worktree.ClaimArgs{Name: in.Name})
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	a.refresh()
	return jsonResult(res)
}

func (a *app) handleRelease(raw json.RawMessage) ext.ToolResult {
	var in nameArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult("invalid args: " + err.Error())
	}
	env, err := a.env()
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	env.RepoRoot = in.RepoRoot
	res, err := a.mgr.Release(env, worktree.ReleaseArgs{Name: in.Name})
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	a.refresh()
	return jsonResult(res)
}

type removeArgs struct {
	Name         string `json:"name"`
	Force        bool   `json:"force"`
	DeleteBranch bool   `json:"delete_branch"`
	RepoRoot     string `json:"repo_root"`
}

func (a *app) handleRemove(raw json.RawMessage) ext.ToolResult {
	var in removeArgs
	if err := json.Unmarshal(raw, &in); err != nil {
		return ext.TextErrorResult("invalid args: " + err.Error())
	}
	env, err := a.env()
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	env.RepoRoot = in.RepoRoot
	res, err := a.mgr.Remove(env, worktree.RemoveArgs{
		Name: in.Name, Force: in.Force, DeleteBranch: in.DeleteBranch,
	})
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	a.refresh()
	return jsonResult(res)
}

// jsonResult marshals a manager result as the tool's text payload. Returns are
// small by construction (well under MaxToolCallBytes), so we don't bound here.
func jsonResult(v any) ext.ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		return ext.TextErrorResult("encode result: " + err.Error())
	}
	return ext.TextResult(string(b))
}

// --- /worktree panel (human-facing UI; the model never sees it) -------------

// handleCommand backs /worktree: open (or focus) the panel. "/worktree collect"
// opens the merge-back overview; anything else opens the worktree list.
func (a *app) handleCommand(args string) ext.Response {
	mode := "list"
	if strings.EqualFold(strings.TrimSpace(args), "collect") {
		mode = "collect"
	}
	if _, err := a.env(); err != nil {
		return ext.Errorf("worktree: %v", err)
	}
	a.mu.Lock()
	a.panelOpen = true
	a.mode = mode
	a.mu.Unlock()
	if title, lines, footer, ok := a.snapshot(); ok {
		return ext.OpenPanel(panelID, title, lines, footer)
	}
	return ext.Errorf("worktree: could not read worktrees")
}

// snapshot re-fetches for the current mode, caches the result, updates the status
// segment, and returns what to render. ok is false on error.
func (a *app) snapshot() (title string, lines []string, footer string, ok bool) {
	env, err := a.env()
	if err != nil {
		return "", nil, "", false
	}
	a.mu.Lock()
	mode := a.mode
	a.mu.Unlock()

	if mode == "collect" {
		res, err := a.mgr.Collect(env)
		if err != nil {
			a.e.Logf("collect: %v", err)
			return "", nil, "", false
		}
		a.mu.Lock()
		a.lastCollect = res
		a.mu.Unlock()
		return worktree.CollectTitle(res), worktree.CollectLines(res), worktree.CollectFooter(), true
	}

	res, err := a.mgr.List(env, worktree.ListFilter{})
	if err != nil {
		a.e.Logf("panel refresh: %v", err)
		return "", nil, "", false
	}
	a.mu.Lock()
	a.last = res
	if a.selected >= len(res.Worktrees) {
		a.selected = len(res.Worktrees) - 1
	}
	if a.selected < 0 {
		a.selected = 0
	}
	sel := a.selected
	a.mu.Unlock()
	a.e.SetStatus("terva-git-worktree", worktree.StatusGlance(res))
	return worktree.PanelTitle(res), worktree.PanelLines(res, sel), worktree.PanelFooter(), true
}

// refresh re-fetches and, if the panel is open, re-renders it. Also keeps the
// status segment current. Safe from any goroutine.
func (a *app) refresh() {
	title, lines, footer, ok := a.snapshot()
	if !ok {
		return
	}
	a.mu.Lock()
	open := a.panelOpen
	a.mu.Unlock()
	if open {
		a.e.RenderPanel(panelID, title, lines, footer)
	}
}

// render re-draws the list panel from the cached snapshot — no git work — used
// for in-panel navigation (list mode only).
func (a *app) render() {
	a.mu.Lock()
	res, sel, open, mode := a.last, a.selected, a.panelOpen, a.mode
	a.mu.Unlock()
	if !open || mode == "collect" || res == nil {
		return
	}
	a.e.RenderPanel(panelID, worktree.PanelTitle(res), worktree.PanelLines(res, sel), worktree.PanelFooter())
}

func (a *app) handleKey(key, text string) {
	switch {
	case key == "up":
		a.mu.Lock()
		if a.mode != "collect" && a.selected > 0 {
			a.selected--
		}
		a.mu.Unlock()
		a.render()
	case key == "down":
		a.mu.Lock()
		n := 0
		if a.last != nil {
			n = len(a.last.Worktrees)
		}
		if a.mode != "collect" && a.selected < n-1 {
			a.selected++
		}
		a.mu.Unlock()
		a.render()
	case key == "enter":
		// Switch the host into the selected worktree via /cd. Only the host can
		// change cwd; we ask it to as if the user typed the command.
		a.mu.Lock()
		var path string
		if a.mode != "collect" && a.last != nil && a.selected >= 0 && a.selected < len(a.last.Worktrees) {
			path = a.last.Worktrees[a.selected].Path
		}
		a.mu.Unlock()
		if path != "" {
			a.e.SubmitSlash("/cd " + path)
		}
	case key == "rune" && strings.EqualFold(text, "c"):
		a.mu.Lock()
		a.mode = "collect"
		a.mu.Unlock()
		a.refresh()
	case key == "rune" && strings.EqualFold(text, "l"):
		a.mu.Lock()
		a.mode = "list"
		a.mu.Unlock()
		a.refresh()
	case key == "rune" && strings.EqualFold(text, "r"):
		a.refresh()
	}
}

func (a *app) handleClose() {
	a.mu.Lock()
	a.panelOpen = false
	a.mu.Unlock()
}
