module terva-git-worktree

go 1.22

// The terva Go SDK is the public extension-author surface. We pin a released
// version so the template builds anywhere with network. To develop against a
// local terva checkout (e.g. to use unreleased SDK features), add a replace:
//
//	go mod edit -replace terva.sh/terva=../terva
//
// and drop it before releasing (`just release-overlay vX.Y.Z` does both).
require terva.sh/terva v0.105.2
