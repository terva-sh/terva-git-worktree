package main

import (
	"encoding/json"
	"fmt"

	"terva-git-worktree/internal/worktree"

	"terva.sh/terva/packages/agent/ext"
)

// app is the thin SDK glue over the pure worktree.Manager: it parses tool args,
// resolves the host environment (cwd / data dir / session), calls the manager,
// and returns the manager's result as JSON the model can parse. All real logic
// lives in internal/worktree.
type app struct {
	e   *ext.Extension
	mgr *worktree.Manager
}

func newApp(e *ext.Extension) *app {
	return &app{e: e, mgr: worktree.NewManager()}
}

// onSession exists so the extension subscribes to session_start; the SDK updates
// Host().SessionID (our claim owner) before this runs, so the body has nothing
// to do — state is keyed per repo, not per session. Log for debugging only.
func (a *app) onSession(s ext.Session) {
	a.e.Logf("active session: %q", s.ID)
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
	return jsonResult(res)
}

type listArgs struct {
	IncludeStale bool `json:"include_stale"`
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
	res, err := a.mgr.List(env, in.IncludeStale)
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
	return jsonResult(res)
}

type removeArgs struct {
	Name         string `json:"name"`
	Force        bool   `json:"force"`
	DeleteBranch bool   `json:"delete_branch"`
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
	res, err := a.mgr.Remove(env, worktree.RemoveArgs{
		Name: in.Name, Force: in.Force, DeleteBranch: in.DeleteBranch,
	})
	if err != nil {
		return ext.TextErrorResult(err.Error())
	}
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
