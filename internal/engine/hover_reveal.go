package engine

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// HoverReveal describes an element that only shows on hover (per-row
// buttons styled with :hover) and where the mouse goes to reveal it.
type HoverReveal struct {
	// HiddenBy is "visibility", "opacity" or "display" ("" when the element
	// is already shown).
	HiddenBy string
	// X, Y is the hover point: the element's own centre when it has a box
	// (the pointer then sits over the row that contains it), else the
	// centre of its nearest rendered ancestor.
	X, Y float64
	// Ancestor describes that ancestor when the element has no box.
	Ancestor string
}

// hoverRevealJS reports how the element is hidden and scrolls the hover
// point into view. Opacity is checked on every ancestor (it is not
// inherited); visibility is inherited, so the element's own value covers
// its ancestors.
const hoverRevealJS = `() => {
	const el = this;
	if (!el.isConnected) return { hiddenBy: "", detached: true };
	let hiddenBy = "";
	for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
		const s = window.getComputedStyle(n);
		if (s.display === "none") { hiddenBy = "display"; break; }
	}
	if (!hiddenBy && window.getComputedStyle(el).visibility === "hidden") hiddenBy = "visibility";
	if (!hiddenBy) {
		for (let n = el; n && n.nodeType === 1; n = n.parentElement) {
			if (Number(window.getComputedStyle(n).opacity) === 0) { hiddenBy = "opacity"; break; }
		}
	}
	let host = el, rect = el.getBoundingClientRect(), ancestor = "";
	if (rect.width <= 0 || rect.height <= 0) {
		host = null;
		for (let n = el.parentElement; n && n.nodeType === 1; n = n.parentElement) {
			const r = n.getBoundingClientRect();
			const s = window.getComputedStyle(n);
			if (r.width > 0 && r.height > 0 && s.display !== "none" && s.visibility !== "hidden") { host = n; break; }
		}
		if (!host) return { hiddenBy, noBox: true };
		ancestor = host.tagName.toLowerCase() + (host.id ? "#" + host.id : "") + (typeof host.className === "string" && host.className.trim() ? "." + host.className.trim().split(/\s+/).slice(0, 2).join(".") : "");
	}
	host.scrollIntoView({ block: "center", inline: "nearest" });
	rect = host.getBoundingClientRect();
	return { hiddenBy, x: rect.x + rect.width / 2, y: rect.y + rect.height / 2, ancestor };
}`

// InspectHoverReveal tells whether el is hidden by CSS and where hovering
// would reveal it, after scrolling that point into view.
func InspectHoverReveal(el *rod.Element) (*HoverReveal, error) {
	res, err := el.Eval(hoverRevealJS)
	if err != nil {
		return nil, fmt.Errorf("hover reveal: %w", err)
	}
	var out struct {
		HiddenBy string  `json:"hiddenBy"`
		Detached bool    `json:"detached"`
		NoBox    bool    `json:"noBox"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Ancestor string  `json:"ancestor"`
	}
	if err := res.Value.Unmarshal(&out); err != nil {
		return nil, fmt.Errorf("hover reveal: %w", err)
	}
	if out.Detached {
		return nil, fmt.Errorf("hover reveal: element is detached")
	}
	if out.NoBox {
		return nil, fmt.Errorf("hover reveal: neither the element nor any ancestor is rendered (hidden by %s)", out.HiddenBy)
	}
	return &HoverReveal{HiddenBy: out.HiddenBy, X: out.X, Y: out.Y, Ancestor: out.Ancestor}, nil
}

// HoverToReveal moves the real mouse to the reveal point so :hover rules of
// the element and its ancestors apply.
func HoverToReveal(page *rod.Page, r *HoverReveal) error {
	if err := page.Mouse.MoveTo(proto.Point{X: r.X, Y: r.Y}); err != nil {
		return fmt.Errorf("hover: %w", err)
	}
	return nil
}
