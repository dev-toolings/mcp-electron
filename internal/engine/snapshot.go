package engine

import (
	"sort"
	"strconv"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// PageSnapshot stores the last known interactive refs for a page target.
// It lives in memory for the lifetime of the MCP server: refs handed out by
// one tool call stay valid for the next until the page changes.
type PageSnapshot struct {
	TargetID string                 `json:"target_id"`
	URL      string                 `json:"url,omitempty"`
	Title    string                 `json:"title,omitempty"`
	Refs     map[string]RefSnapshot `json:"refs,omitempty"`
	// CachedExtraction is the last ExtractionResult for this page, so a
	// consumer can reuse it without a second AccessibilityGetFullAXTree call.
	CachedExtraction *ExtractionResult `json:"cached_extraction,omitempty"`
}

// RefSnapshot stores a stable backend node mapping for a single ref.
type RefSnapshot struct {
	BackendNodeID proto.DOMBackendNodeID `json:"backend_node_id"`
	Role          string                 `json:"role,omitempty"`
	Name          string                 `json:"name,omitempty"`
	// Nth is the 0-based occurrence of role+name among interactive nodes
	// at snapshot time. Used to re-resolve after a SPA rerender.
	Nth  int    `json:"nth,omitempty"`
	Href string `json:"href,omitempty"`

	FrameID proto.PageFrameID `json:"frame_id,omitempty"`
}

// BuildSnapshot creates an in-memory ref snapshot from an extraction result.
func BuildSnapshot(page *rod.Page, result *ExtractionResult) (*PageSnapshot, error) {
	info, err := page.Info()
	if err != nil {
		return nil, err
	}
	snapshot := &PageSnapshot{
		TargetID:         string(page.TargetID),
		Refs:             assignRefSnapshots(result.Refs),
		CachedExtraction: result,
	}
	if info != nil {
		snapshot.URL = info.URL
		snapshot.Title = info.Title
	}
	return snapshot, nil
}

// SnapshotRefNum parses the number of an "@N" ref.
func SnapshotRefNum(ref string) (int, bool) {
	return snapshotRefNum(ref)
}

func snapshotRefNum(ref string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(ref, "@"))
	return n, err == nil
}

func assignRefSnapshots(refs map[string]ExtractedNode) map[string]RefSnapshot {
	out := map[string]RefSnapshot{}
	keys := make([]string, 0, len(refs))
	for ref := range refs {
		keys = append(keys, ref)
	}
	sort.Slice(keys, func(i, j int) bool {
		ni, iok := snapshotRefNum(keys[i])
		nj, jok := snapshotRefNum(keys[j])
		if iok && jok {
			return ni < nj
		}
		return keys[i] < keys[j]
	})
	nthByKey := map[string]int{}
	for _, ref := range keys {
		node := refs[ref]
		if node.BackendNodeID == 0 {
			continue
		}
		key := strings.ToLower(node.Role) + "|" + strings.ToLower(strings.TrimSpace(node.Name))
		nth := nthByKey[key]
		nthByKey[key] = nth + 1
		out[ref] = RefSnapshot{BackendNodeID: node.BackendNodeID, Role: node.Role, Name: node.Name, Nth: nth, Href: node.Href, FrameID: node.FrameID}
	}
	return out
}
