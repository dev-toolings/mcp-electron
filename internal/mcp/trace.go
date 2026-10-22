package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// The trace is for the agent that drives the app: when a call fails, hangs or is interrupted by
// the host, the agent reads what happened (trace tool, or the JSONL file) and can fix the server
// itself. One line when a call starts, one when it ends, one when the host cancels it, and a dump
// of the server's goroutines when it is still running after StuckAfter.

const (
	traceEnv        = "MCP_ELECTRON_TRACE"
	traceMaxBytes   = 16 << 20
	traceStuckAfter = 20 * time.Second
	traceTail       = 4 << 20
	traceResult     = 600
	traceStack      = 64 << 10
)

// Tracer appends JSON lines to one file, rotated once at traceMaxBytes. A nil Tracer does nothing.
type Tracer struct {
	path       string
	stuckAfter time.Duration
	mu         sync.Mutex
	file       *os.File
	size       int64
	seq        atomic.Int64
}

// tracePath is MCP_ELECTRON_TRACE (a file path, or "off"), else <user cache>/mcp-electron/trace.jsonl.
func tracePath() string {
	if v := strings.TrimSpace(os.Getenv(traceEnv)); v != "" {
		if strings.EqualFold(v, "off") || v == "0" {
			return ""
		}
		return v
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "mcp-electron", "trace.jsonl")
}

// NewTracer opens the trace file; on any error the server runs without trace (said on stderr).
func NewTracer(path string) *Tracer {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		logf("trace disabled: %v", err)
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		logf("trace disabled: %v", err)
		return nil
	}
	info, _ := f.Stat()
	t := &Tracer{path: path, stuckAfter: traceStuckAfter, file: f}
	if info != nil {
		t.size = info.Size()
	}
	return t
}

// Path is where the trace is written ("" without trace).
func (t *Tracer) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

func (t *Tracer) write(rec map[string]any) {
	if t == nil {
		return
	}
	rec["ts"] = time.Now().Format(time.RFC3339Nano)
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	line = append(line, '\n')
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file == nil {
		return
	}
	if t.size+int64(len(line)) > traceMaxBytes {
		t.file.Close()
		_ = os.Rename(t.path, t.path+".1")
		f, err := os.OpenFile(t.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			t.file = nil
			return
		}
		t.file, t.size = f, 0
	}
	n, _ := t.file.Write(line)
	t.size += int64(n)
}

// Close flushes nothing (writes are unbuffered) and closes the file.
func (t *Tracer) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.file != nil {
		t.file.Close()
		t.file = nil
	}
}

// traceArgs keeps what explains a call without copying what it typed: text is reduced to its
// length, long strings are clipped.
func traceArgs(tool string, args map[string]any) map[string]any {
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch s := v.(type) {
		case string:
			if k == "text" && (tool == "type" || tool == "fill_form") {
				out[k] = fmt.Sprintf("<%d chars>", len([]rune(s)))
			} else if len(s) > 300 {
				out[k] = s[:300] + "…"
			} else {
				out[k] = s
			}
		default:
			out[k] = v
		}
	}
	return out
}

func resultText(res *mcpgo.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			b.WriteString(tc.Text)
			b.WriteByte('\n')
		} else {
			b.WriteString("<non-text content>\n")
		}
		if b.Len() > traceResult {
			break
		}
	}
	s := strings.TrimSpace(b.String())
	if len(s) > traceResult {
		s = s[:traceResult] + "…"
	}
	return s
}

// goroutines returns the stacks of every goroutine, the server's own first, bounded.
func goroutines() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	blocks := bytes.Split(buf, []byte("\n\n"))
	var own, rest []string
	for _, b := range blocks {
		if bytes.Contains(b, []byte("mcp-electron/internal/")) {
			own = append(own, string(b))
		} else {
			rest = append(rest, string(b))
		}
	}
	s := strings.Join(append(own, rest...), "\n\n")
	if len(s) > traceStack {
		s = s[:traceStack] + "\n…"
	}
	return s
}

// Wrap traces one tool handler: start, end (or panic), host cancellation, and a stuck dump.
func (t *Tracer) Wrap(tool string, h func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error)) func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	if t == nil {
		return h
	}
	return func(ctx context.Context, req mcpgo.CallToolRequest) (res *mcpgo.CallToolResult, err error) {
		id, start := t.seq.Add(1), time.Now()
		t.write(map[string]any{"ev": "start", "id": id, "tool": tool, "args": traceArgs(tool, req.GetArguments())})
		done := make(chan struct{})
		go func() {
			stuck := time.NewTimer(t.stuckAfter)
			defer stuck.Stop()
			cancelled := ctx.Done()
			for {
				select {
				case <-done:
					return
				case <-cancelled:
					t.write(map[string]any{"ev": "cancelled", "id": id, "tool": tool, "ms": time.Since(start).Milliseconds(), "reason": fmt.Sprint(ctx.Err())})
					// Keep watching: a handler that ignores the cancellation still shows up as stuck.
					cancelled = nil
				case <-stuck.C:
					t.write(map[string]any{"ev": "stuck", "id": id, "tool": tool, "ms": time.Since(start).Milliseconds(), "goroutines": goroutines()})
					return
				}
			}
		}()
		defer func() {
			close(done)
			rec := map[string]any{"ev": "end", "id": id, "tool": tool, "ms": time.Since(start).Milliseconds()}
			if p := recover(); p != nil {
				rec["panic"] = fmt.Sprint(p)
				rec["stack"] = string(debugStack())
				t.write(rec)
				panic(p)
			}
			if err != nil {
				rec["error"] = err.Error()
			}
			if res != nil {
				rec["is_error"] = res.IsError
				rec["result"] = resultText(res)
			}
			t.write(rec)
		}()
		return h(ctx, req)
	}
}

func debugStack() []byte {
	buf := make([]byte, traceStack)
	return buf[:runtime.Stack(buf, false)]
}

// traceQuery filters the tail of the trace for the trace tool.
type traceQuery struct {
	last       int
	tool       string
	problems   bool
	since      time.Duration
	withStacks bool
}

// Read returns the matching lines of the last traceTail bytes, oldest first, at most q.last.
func (t *Tracer) Read(q traceQuery) ([]map[string]any, error) {
	if t == nil {
		return nil, fmt.Errorf("trace disabled (%s=off or no cache directory)", traceEnv)
	}
	f, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > traceTail {
		if _, err := f.Seek(info.Size()-traceTail, io.SeekStart); err != nil {
			return nil, err
		}
	}
	var cutoff time.Time
	if q.since > 0 {
		cutoff = time.Now().Add(-q.since)
	}
	// A call's start line is kept when its end says it failed, so the arguments come with the error.
	var all []map[string]any
	failed := map[float64]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for sc.Scan() {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if !cutoff.IsZero() {
			if ts, err := time.Parse(time.RFC3339Nano, fmt.Sprint(rec["ts"])); err == nil && ts.Before(cutoff) {
				continue
			}
		}
		if q.tool != "" && rec["tool"] != q.tool {
			continue
		}
		if isProblem(rec) {
			if id, ok := rec["id"].(float64); ok {
				failed[id] = true
			}
		}
		all = append(all, rec)
	}
	var out []map[string]any
	for _, rec := range all {
		if q.problems && !isProblem(rec) {
			id, _ := rec["id"].(float64)
			if rec["ev"] != "start" || !failed[id] {
				continue
			}
		}
		if !q.withStacks {
			if _, ok := rec["goroutines"]; ok {
				rec["goroutines"] = "<omitted: with_stacks true>"
			}
		}
		out = append(out, rec)
	}
	if q.last > 0 && len(out) > q.last {
		out = out[len(out)-q.last:]
	}
	return out, nil
}

func isProblem(rec map[string]any) bool {
	switch rec["ev"] {
	case "stuck", "cancelled":
		return true
	case "end":
		if rec["is_error"] == true {
			return true
		}
		if _, ok := rec["error"]; ok {
			return true
		}
		_, ok := rec["panic"]
		return ok
	}
	return false
}
