package engine

import (
	"fmt"
	"strings"

	"github.com/go-rod/rod/lib/proto"
)

// EvadeRuntimeEnable reports whether Runtime.enable must be avoided. The
// Electron edition never drives a hostile site: Runtime events (console,
// exceptions) are exactly what a developer wants, so evasion is always off.
func EvadeRuntimeEnable() bool { return false }

func flattenHeaders(h proto.NetworkHeaders) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		switch vv := v.Val().(type) {
		case string:
			out[k] = vv
		default:
			out[k] = strings.TrimSpace(fmt.Sprintf("%v", vv))
		}
	}
	return out
}

// NetworkEntry represents a captured network request.
type NetworkEntry struct {
	Method   string `json:"method,omitempty"`
	URL      string `json:"url"`
	Status   int    `json:"status"`
	Size     int    `json:"size_bytes"`
	TimeMs   int64  `json:"time_ms"`
	MimeType string `json:"mime_type,omitempty"`
	Error    string `json:"error,omitempty"`
}
