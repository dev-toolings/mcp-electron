package engine

import (
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// WheelAt sends real mouse-wheel events at (x, y): dy pixels in total (positive scrolls down), split into steps
// events interval apart. Unlike ScrollBy (window.scrollBy), the page receives `wheel` events and the scroll
// container under the pointer scrolls, as with a trackpad or a mouse; fast bursts reproduce quick flicks.
func WheelAt(page *rod.Page, x, y float64, dy float64, steps int, interval time.Duration) error {
	if steps <= 0 {
		steps = 1
	}
	if err := page.Mouse.MoveTo(proto.NewPoint(x, y)); err != nil {
		return fmt.Errorf("move to wheel point: %w", err)
	}
	delta := dy / float64(steps)
	for i := 0; i < steps; i++ {
		if i > 0 && interval > 0 {
			time.Sleep(interval)
		}
		if err := (proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseWheel, X: x, Y: y, DeltaY: delta}).Call(page); err != nil {
			return fmt.Errorf("wheel step %d: %w", i+1, err)
		}
	}
	settleAfterAction(page, 0)
	return nil
}
