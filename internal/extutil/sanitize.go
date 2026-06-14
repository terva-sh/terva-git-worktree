// Package extutil holds small, extension-agnostic helpers reusable across terva
// extensions: one-line input sanitization (defuses display injection and bounds
// size) and traversal-safe per-session filenames. Keep these in any extension
// that renders model/host/user text or keys files per session.
package extutil

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// CleanOneLine collapses a value to a single safe display line: newlines, tabs,
// and other control characters (including ANSI escapes) are dropped or turned
// into spaces, runs of whitespace are collapsed, the result is trimmed, and it
// is truncated to max runes with an ellipsis. Apply it to every model/host/user
// string you render or persist, so a field can't inject extra lines or escape
// sequences into tool output or the panel. max <= 0 means no length limit.
func CleanOneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1 // drop other control chars (e.g. ESC)
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 {
		if r := []rune(s); len(r) > max {
			return strings.TrimSpace(string(r[:max])) + "…"
		}
	}
	return s
}

// SafeSessionID reports whether id can be used verbatim in a filename: a
// non-empty run of [A-Za-z0-9._-] (<=128) with no "..", and not "." or "..".
func SafeSessionID(id string) bool {
	if id == "" || id == "." || id == ".." || len(id) > 128 || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// SessionFileName maps a session id to a traversal-safe "<prefix>-<id>.json".
// A path-safe id (the documented UUID contract) is used verbatim; anything else
// is hashed, so a hostile session id can never escape the data dir.
func SessionFileName(prefix, id string) string {
	if SafeSessionID(id) {
		return prefix + "-" + id + ".json"
	}
	sum := sha256.Sum256([]byte(id))
	return prefix + "-" + hex.EncodeToString(sum[:8]) + ".json"
}
