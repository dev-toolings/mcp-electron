package engine

import "testing"

// Attaching must not emulate a device: the page keeps the window's own
// viewport and device scale factor.
func TestConnectDoesNotEmulateDevice(t *testing.T) {
	_, page := newIsolatedPage(t)
	page.MustNavigate(dataURL(`<p>viewport</p>`)).MustWaitLoad()
	res := page.MustEval(`() => ({w: innerWidth, h: innerHeight, ow: outerWidth, dpr: devicePixelRatio})`)
	w, h, ow := res.Get("w").Int(), res.Get("h").Int(), res.Get("ow").Int()
	if w == 1280 && h == 800 && ow != 1280 {
		t.Fatalf("rod's default 1280x800 device is emulated: %s", res.JSON("", ""))
	}
	if w > ow {
		t.Fatalf("viewport wider than the window, an emulation is active: %s", res.JSON("", ""))
	}
}
