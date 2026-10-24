package electron

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Placeholders a development command may use in its argv or environment to
// receive the ports this server chose. They are also exported as environment
// variables of the same name, so a dev script can read them without any
// placeholder: pass --remote-debugging-port=$MCP_ELECTRON_CDP_PORT and
// --inspect=127.0.0.1:$MCP_ELECTRON_INSPECT_PORT to the electron binary.
const (
	EnvCDPPort     = "MCP_ELECTRON_CDP_PORT"
	EnvInspectPort = "MCP_ELECTRON_INSPECT_PORT"
)

// freePort asks the kernel for an unused loopback TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// expandPorts substitutes ${MCP_ELECTRON_CDP_PORT} / ${MCP_ELECTRON_INSPECT_PORT}
// (and the $NAME form) in s.
func expandPorts(s string, cdpPort, inspectPort int) string {
	r := strings.NewReplacer(
		"${"+EnvCDPPort+"}", strconv.Itoa(cdpPort),
		"$"+EnvCDPPort, strconv.Itoa(cdpPort),
		"${"+EnvInspectPort+"}", strconv.Itoa(inspectPort),
		"$"+EnvInspectPort, strconv.Itoa(inspectPort),
	)
	return r.Replace(s)
}

// probeTimeout bounds one liveness probe of a debugging endpoint.
const probeTimeout = 1500 * time.Millisecond

// versionInfo is the answer of /json/version.
type versionInfo struct {
	Browser   string `json:"Browser"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}

// probeCDP reports whether a Chromium remote debugging endpoint answers at
// endpoint (http://127.0.0.1:PORT). A Node inspector also answers
// /json/version, with Browser "node.js/…": it is not a CDP browser endpoint.
func probeCDP(ctx context.Context, endpoint string) (versionInfo, bool) {
	var v versionInfo
	// A ws:// endpoint (the pid-attach bridge) answers /json/version under its own secret path.
	if rest, ok := strings.CutPrefix(endpoint, "ws://"); ok {
		endpoint = "http://" + rest
	}
	if err := readJSON(ctx, strings.TrimRight(endpoint, "/")+"/json/version", &v); err != nil {
		return v, false
	}
	if v.Browser == "" || strings.HasPrefix(v.Browser, "node.js/") {
		return v, false
	}
	return v, true
}

// inspectorTarget is one entry of a Node inspector's /json/list.
type inspectorTarget struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}

// probeInspector returns the WebSocket URL of a Node inspector listening at
// endpoint, or "" when none answers.
func probeInspector(ctx context.Context, endpoint string) string {
	var list []inspectorTarget
	if err := readJSON(ctx, strings.TrimRight(endpoint, "/")+"/json/list", &list); err != nil {
		return ""
	}
	for _, t := range list {
		if t.Type == "node" && t.WebSocket != "" {
			return t.WebSocket
		}
	}
	return ""
}

func readJSON(ctx context.Context, url string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// Chromium requires a Host header that is an IP or localhost: a loopback
	// URL already satisfies that.
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20))
	return dec.Decode(out)
}

// loopbackEndpoint builds http://127.0.0.1:PORT.
func loopbackEndpoint(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }
