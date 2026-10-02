package store

import (
	"encoding/json"
	"strconv"
	"strings"
)

// CRMSearchHit is one leaf value inside a CRM+ entity's content tree that
// matched a search query — the CRM+ equivalent of SearchHit (jsondata.go).
// Label is a human-readable breadcrumb through the tree (e.g. "testimonials
// › #2 › quote") built from each ancestor node's own key, or its position
// among its siblings when it has none (a list item). NodePath is the same
// match addressed as [index, "children", index, ...] — the convention the
// entity editor's own getNodeByPath already uses (see
// templates/crm_entity_editor.html) — so a search result can deep-link
// straight to the matched field instead of just naming it.
type CRMSearchHit struct {
	Label    string
	NodePath []any
	Value    string
}

// SearchCRMEntityContent scans every leaf value in a CRM+ entity's
// content_json tree for a case-insensitive substring match, returning at
// most limit hits (0 means unlimited). The tree is the same loosely-typed
// {type, key, value, children} shape the entity editor and AI design
// generation already decode (see signCRMImageNodes/truncateForPrompt in
