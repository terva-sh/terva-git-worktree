// Command terva-git-worktree is a terva extension that lets the agent create,
// list, and remove git worktrees for the current repository and reason about
// reuse via an available/claimed model plus the commit each worktree was
// branched from. Phase 1: the three agent tools (worktree_create,
// worktree_list, worktree_remove). The human-facing /worktree panel and the
// swarm integration are later phases.
//
// Pure logic lives in internal/worktree (unit-tested without the SDK); this file
// is registration only, and app.go is the thin SDK glue.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/ext"
)

func main() {
	e := ext.New("terva-git-worktree", "0.4.0")

	// Require protocol 2: we depend on session identity (claim owner) and
	// per-extension data dir / cwd from the handshake. An older host refuses to
	// load us with a clear message instead of half-speaking the wire.
	e.RequireProtocol(2)

	// Standing policy folded into the cached system prompt, so the model knows
	// when to reach for these tools without us paying tokens on a context card
	// every turn. Tool descriptions restate the essentials in case a user opts
	// out of context injection.
	e.ContributeContext(contextPolicy)

	a := newApp(e)

	// worktree_list is the agent's pre-decision call and never mutates (beyond
	// reconciling git's own bookkeeping), so declare it read-only: the host
	// admits it without a prompt in read-only/workspace approval modes. create
	// and remove are side-effecting and (correctly) ask in workspace mode.
	e.Tool("worktree_list", descList, schemaList(), a.handleList, ext.ReadOnly())
	e.Tool("worktree_create", descCreate, schemaCreate(), a.handleCreate)
	e.Tool("worktree_claim", descClaim, schemaName("name of an existing worktree to claim"), a.handleClaim)
	e.Tool("worktree_release", descRelease, schemaName("name of the worktree to release"), a.handleRelease)
	e.Tool("worktree_remove", descRemove, schemaRemove(), a.handleRemove)

	// Human-facing UI: /worktree opens an interactive panel of the repo's
	// worktrees (the model never sees it). Panel keys drive selection/refresh.
	e.Command("worktree", "worktree panel (`/worktree collect` for the merge-back view)", a.handleCommand)
	e.OnPanelKey(panelID, a.handleKey, a.handleClose)

	// Subscribe to session_start so the host delivers it and the SDK keeps
	// Host().SessionID current — that's the claim-owner identity our tools read
	// per call. The handler itself is a no-op (registry is keyed per repo, not
	// per session); subscribing is the point. Delivered in headless modes too.
	e.OnSession(a.onSession)

	if err := e.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

const contextPolicy = "You can manage git worktrees for the current repository " +
	"with five tools. All five operate on the cwd's repo by default; pass " +
	"`repo_root` (a path) to target a different repo without a /cd — useful when " +
	"cwd isn't itself a git repo but a checkout sits nearby. Call worktree_list " +
	"FIRST when you might need an isolated checkout: it reports every worktree as " +
	"`available` or `claimed`, the commit it was branched from " +
	"(base_commit/base_ref) and its current HEAD, whether it's dirty, and which " +
	"one you're currently in — so you can reuse a suitable available worktree " +
	"instead of proliferating new ones (pass `match` to filter, e.g. available " +
	"worktrees branched from main). worktree_create makes (or, if the name already " +
	"exists and is available, reuses) a worktree and claims it for this session; " +
	"pass `base` to branch from a specific ref. worktree_claim takes an existing " +
	"available worktree for this session and worktree_release frees your claim — " +
	"together they hand an idle worktree between agents without creating or " +
	"deleting one. worktree_remove deletes one — it refuses when the worktree has " +
	"uncommitted or unmerged/unpushed work unless you pass force:true, and leaves " +
	"the branch unless you pass delete_branch:true. Worktrees live under the " +
	"extension's own data dir, never inside the repo."

// Tool descriptions stay terse (full policy is in contextPolicy) but keep the
// essentials so the tools are usable when context injection is disabled.

const descList = "List git worktrees for the current repo. Read-only. Returns " +
	"JSON: each worktree's name, path, branch, base_commit/base_ref, head_commit, " +
	"status (available|claimed), claimed_by (self|<session>|null), stale_reason, " +
	"dirty, and unmanaged; plus repo_key and cwd_worktree (the one you're in, or " +
	"null). Optional `match` filters the results by {status, base_ref, mine} " +
	"(e.g. available worktrees branched from main). Call this before " +
	"worktree_create to reuse an existing worktree."

const descCreate = "Create (or reuse) a git worktree and claim it for this " +
	"session. Provide `name` (slugged; becomes branch wt/<name>). Optional " +
	"`base` (ref/SHA to branch from; default current HEAD) and " +
	"`reuse_if_available` (default true: if <name> exists and is available, claim " +
	"and return it instead of erroring). Returns the worktree JSON incl. " +
	"`reused`. Errors if <name> is claimed by another live session."

const descClaim = "Claim an existing available worktree for this session by " +
	"`name`, without creating one — use it to take over an idle worktree another " +
	"agent left (see worktree_list). Idempotent if you already hold it; errors if " +
	"it is claimed by another live session. Returns the worktree JSON."

const descRelease = "Release this session's claim on a worktree by `name` so " +
	"another agent can take it, without removing the worktree. Clears a stale " +
	"claim too; errors only if the worktree is held by another live session. " +
	"Returns JSON { name, released, status }."

const descRemove = "Remove a managed git worktree by `name`. Refuses if it has " +
	"uncommitted changes or unmerged/unpushed commits unless `force` is true. " +
	"Leaves the branch by default; set `delete_branch` to also delete wt/<name>. " +
	"Returns JSON { name, removed, branch_deleted }."

// repoRootDesc documents the optional repo_root escape hatch shared by every
// tool. The tools are cwd-first; repo_root targets a different repo without a
// /cd (e.g. when cwd isn't a git repo but a checkout sits nearby).
const repoRootDesc = "optional path to the target git repo (absolute, or " +
	"relative to cwd) to operate on instead of the cwd's repo; omit to use cwd. " +
	"cwd_worktree still reflects your real cwd, not this override"

func repoRootProp() map[string]any {
	return map[string]any{"type": "string", "description": repoRootDesc}
}

func schemaCreate() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":               map[string]any{"type": "string", "description": "worktree name; slugged into branch wt/<name>"},
			"base":               map[string]any{"type": "string", "description": "ref or SHA to branch from (default: current HEAD)"},
			"reuse_if_available": map[string]any{"type": "boolean", "description": "if <name> exists and is available, claim and return it (default true)"},
			"repo_root":          repoRootProp(),
		},
		"required": []string{"name"},
	})
	return b
}

func schemaList() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"match": map[string]any{
				"type":        "object",
				"description": "optional filter for the returned worktrees (does not affect cwd_worktree)",
				"properties": map[string]any{
					"status":   map[string]any{"type": "string", "enum": []string{"available", "claimed"}, "description": "only worktrees with this status"},
					"base_ref": map[string]any{"type": "string", "description": "only worktrees branched from this ref"},
					"mine":     map[string]any{"type": "boolean", "description": "only worktrees claimed by this session"},
				},
			},
			"repo_root": repoRootProp(),
		},
	})
	return b
}

// schemaName builds the {name, repo_root} schema shared by the single-argument
// tools (claim / release).
func schemaName(desc string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":      map[string]any{"type": "string", "description": desc},
			"repo_root": repoRootProp(),
		},
		"required": []string{"name"},
	})
	return b
}

func schemaRemove() json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":          map[string]any{"type": "string", "description": "name of the worktree to remove"},
			"force":         map[string]any{"type": "boolean", "description": "remove even with uncommitted or unmerged/unpushed work"},
			"delete_branch": map[string]any{"type": "boolean", "description": "also delete the wt/<name> branch (default false)"},
			"repo_root":     repoRootProp(),
		},
		"required": []string{"name"},
	})
	return b
}
