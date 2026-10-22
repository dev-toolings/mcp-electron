package engine

import (
	"testing"

	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

func axStr(s string) *proto.AccessibilityAXValue {
	return &proto.AccessibilityAXValue{Type: proto.AccessibilityAXValueTypeString, Value: gson.New(s)}
}

func axRole(s string) *proto.AccessibilityAXValue {
	return &proto.AccessibilityAXValue{Type: proto.AccessibilityAXValueTypeRole, Value: gson.New(s)}
}

// radixMenuTree mirrors the accessibility tree Chrome reports while a
// Radix/shadcn DropdownMenu is open in an Electron renderer: the application
// root is aria-hidden (every node of it ignored), the menu lives in a sibling
// portal, and its items are menuitemradio. When orphanPortal is true the
// portal's parent is missing from the flat list, as Chrome does when it
// re-parents the portal outside the page root.
func radixMenuTree(orphanPortal bool) ([]*proto.AccessibilityAXNode, map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode) {
	id := func(s string) proto.AccessibilityAXNodeID { return proto.AccessibilityAXNodeID(s) }
	portalParent := id("page")
	pageChildren := []proto.AccessibilityAXNodeID{id("app"), id("portal")}
	if orphanPortal {
		portalParent = id("detached-wrapper")
		pageChildren = pageChildren[:1]
	}
	nodes := []*proto.AccessibilityAXNode{
		{NodeID: id("page"), Role: axRole("RootWebArea"), Name: axStr("Escape"), BackendDOMNodeID: 1,
			ChildIDs: pageChildren},
		{NodeID: id("app"), ParentID: id("page"), Ignored: true, BackendDOMNodeID: 2,
			ChildIDs: []proto.AccessibilityAXNodeID{id("app-button")}},
		{NodeID: id("app-button"), ParentID: id("app"), Ignored: true, Role: axRole("button"), Name: axStr("Mode : Auto"), BackendDOMNodeID: 3},
		{NodeID: id("portal"), ParentID: portalParent, Role: axRole("generic"), BackendDOMNodeID: 4,
			ChildIDs: []proto.AccessibilityAXNodeID{id("menu")}},
		{NodeID: id("menu"), ParentID: id("portal"), Role: axRole("menu"), Name: axStr("Mode"), BackendDOMNodeID: 5,
			ChildIDs: []proto.AccessibilityAXNodeID{id("item-auto"), id("item-manual")}},
		{NodeID: id("item-auto"), ParentID: id("menu"), Role: axRole("menuitemradio"), Name: axStr("Auto"), BackendDOMNodeID: 6,
			ChildIDs: []proto.AccessibilityAXNodeID{id("item-auto-text")}},
		{NodeID: id("item-auto-text"), ParentID: id("item-auto"), Role: axRole("StaticText"), Name: axStr("Auto"), BackendDOMNodeID: 7},
		{NodeID: id("item-manual"), ParentID: id("menu"), Role: axRole("menuitemradio"), Name: axStr("Manuel"), BackendDOMNodeID: 8},
	}
	nodeMap := map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode{}
	for _, n := range nodes {
		nodeMap[n.NodeID] = n
	}
	return nodes, nodeMap
}

func extractSynthetic(t *testing.T, nodes []*proto.AccessibilityAXNode, nodeMap map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode, level ExtractLevel) *ExtractionResult {
	t.Helper()
	refCounter := 0
	stats := ExtractionStats{TotalNodes: len(nodes)}
	refs := map[string]ExtractedNode{}
	budget := newEnrichmentBudget()
	var out []ExtractedNode
	for _, rootID := range treeRootIDs(nodes, nodeMap) {
		out = append(out, buildTree(nil, nodeMap, rootID, level, &refCounter, refs, &stats, budget)...)
	}
	return &ExtractionResult{Nodes: out, Refs: refs, Stats: stats}
}

func findRole(nodes []ExtractedNode, role string) *ExtractedNode {
	for i := range nodes {
		if nodes[i].Role == role {
			return &nodes[i]
		}
		if found := findRole(nodes[i].Children, role); found != nil {
			return found
		}
	}
	return nil
}

// An open menu over an aria-hidden application root used to come back as bare
// text at the content level: menuitemradio was not a skeleton role, so the
// items were dropped and only their StaticText children survived, with no
// @ref to click. Every level must now show the menu and its items.
func TestExtractOpenRadixMenuOverHiddenRoot(t *testing.T) {
	for _, level := range []ExtractLevel{LevelSkeleton, LevelContent, LevelFull} {
		nodes, nodeMap := radixMenuTree(false)
		result := extractSynthetic(t, nodes, nodeMap, level)

		if len(result.Refs) != 2 {
			t.Fatalf("level %s: expected the two menu items to get refs, got %d (%s)", level, len(result.Refs), FormatText(result))
		}
		if findRole(result.Nodes, "menu") == nil {
			t.Fatalf("level %s: the open menu container must be shown, got:\n%s", level, FormatText(result))
		}
		if hasName(result, "Mode : Auto") {
			t.Fatalf("level %s: the aria-hidden application root must stay out, got:\n%s", level, FormatText(result))
		}
		for _, ref := range []string{"@1", "@2"} {
			if result.Refs[ref].Role != "menuitemradio" {
				t.Fatalf("level %s: %s should be a menuitemradio, got %#v", level, ref, result.Refs[ref])
			}
		}
	}
}

// A portal whose parent is missing from the flat list is still part of the
// page: it must be an extraction root, otherwise the whole snapshot is empty.
// A node whose parent is unknown but that another node lists as a child is
// not an orphan: it is reached through that child list and must not be
// extracted twice.
func TestTreeRootIDsIncludeOrphanedPortal(t *testing.T) {
	linked, linkedMap := radixMenuTree(false)
	linkedMap["portal"].ParentID = "detached-wrapper"
	if roots := treeRootIDs(linked, linkedMap); len(roots) != 1 || roots[0] != "page" {
		t.Fatalf("a node still listed as a child is not an orphan, got %#v", roots)
	}
	if result := extractSynthetic(t, linked, linkedMap, LevelContent); len(result.Refs) != 2 {
		t.Fatalf("expected the two items once, got:\n%s", FormatText(result))
	}

	nodes, nodeMap := radixMenuTree(true)

	roots := treeRootIDs(nodes, nodeMap)
	if len(roots) != 2 || roots[0] != "page" || roots[1] != "portal" {
		t.Fatalf("expected the page root then the orphaned portal, got %#v", roots)
	}

	result := extractSynthetic(t, nodes, nodeMap, LevelContent)
	if len(result.Refs) != 2 || result.Refs["@1"].Name != "Auto" || result.Refs["@2"].Name != "Manuel" {
		t.Fatalf("expected both menu items reachable through the orphaned portal, got:\n%s", FormatText(result))
	}
}
