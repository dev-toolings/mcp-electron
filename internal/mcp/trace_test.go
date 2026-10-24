package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func call(args map[string]any) mcpgo.CallToolRequest {
	var req mcpgo.CallToolRequest
	req.Params.Arguments = args
	return req
}

func TestTraceRecordsCallsCancellationsAndStuckCalls(t *testing.T) {
	tr := NewTracer(filepath.Join(t.TempDir(), "trace.jsonl"))
	defer tr.Close()
	tr.stuckAfter = 50 * time.Millisecond

	ok := tr.Wrap("type", func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return mcpgo.NewToolResultText("typed"), nil
	})
	if _, err := ok(context.Background(), call(map[string]any{"text": "secret", "target": "@1"})); err != nil {
		t.Fatal(err)
	}
	failing := tr.Wrap("click", func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return nil, errors.New("boom")
	})
	_, _ = failing(context.Background(), call(map[string]any{"target": "@2"}))
	// A handler that ignores its context: cancelled by the host, then reported stuck with the goroutines.
	ctx, cancel := context.WithCancel(context.Background())
	hang := tr.Wrap("hover", func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		time.Sleep(150 * time.Millisecond)
		return mcpgo.NewToolResultText("late"), nil
	})
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	_, _ = hang(ctx, call(nil))

	all, err := tr.Read(traceQuery{last: 100, withStacks: true})
	if err != nil {
		t.Fatal(err)
	}
	evs := []string{}
	for _, r := range all {
		evs = append(evs, r["tool"].(string)+":"+r["ev"].(string))
	}
	want := []string{"type:start", "type:end", "click:start", "click:end", "hover:start", "hover:cancelled", "hover:stuck", "hover:end"}
	if len(evs) != len(want) {
		t.Fatalf("events %v, want %v", evs, want)
	}
	for i := range want {
		if evs[i] != want[i] {
			t.Fatalf("events %v, want %v", evs, want)
		}
	}
	if got := all[0]["args"].(map[string]any)["text"]; got != "<6 chars>" {
		t.Fatalf("typed text kept: %v", got)
	}
	if s, _ := all[6]["goroutines"].(string); s == "" {
		t.Fatal("stuck call without goroutines")
	}
	problems, _ := tr.Read(traceQuery{last: 100, problems: true})
	got := []string{}
	for _, r := range problems {
		got = append(got, r["tool"].(string)+":"+r["ev"].(string))
	}
	wantProblems := []string{"click:start", "click:end", "hover:start", "hover:cancelled", "hover:stuck"}
	if len(got) != len(wantProblems) {
		t.Fatalf("problems %v, want %v", got, wantProblems)
	}
	if s, _ := problems[4]["goroutines"].(string); s != "<omitted: with_stacks true>" {
		t.Fatalf("stacks not omitted: %.40q", s)
	}
	if err := tr.Clear(); err != nil {
		t.Fatal(err)
	}
	if left, _ := tr.Read(traceQuery{last: 100}); len(left) != 0 {
		t.Fatalf("cleared trace still has %d lines", len(left))
	}
}

// The host cancels a request's context right after the answer: that must
// not log a cancellation of a call that already ended. And a call that asked
// for a long wait is not stuck while it is within it.
func TestTraceIgnoresCancellationAfterTheEndAndHonoursTimeouts(t *testing.T) {
	tr := NewTracer(filepath.Join(t.TempDir(), "trace.jsonl"))
	defer tr.Close()
	tr.stuckAfter = 50 * time.Millisecond
	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		h := tr.Wrap("status", func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		})
		_, _ = h(ctx, call(nil))
		cancel() // as the host does once the answer is sent
	}
	wait := tr.Wrap("wait_for", func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		time.Sleep(150 * time.Millisecond)
		return mcpgo.NewToolResultText("waited"), nil
	})
	_, _ = wait(context.Background(), call(map[string]any{"timeout_ms": 1000}))
	time.Sleep(20 * time.Millisecond)
	problems, err := tr.Read(traceQuery{last: 1000, problems: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range problems {
		t.Errorf("unexpected problem event %v:%v", r["tool"], r["ev"])
	}
	if d := stuckAfter(20*time.Second, call(map[string]any{"timeout_ms": 120000})); d != 130*time.Second {
		t.Fatalf("stuckAfter for a 2 min wait = %s", d)
	}
	if d := stuckAfter(20*time.Second, call(nil)); d != 20*time.Second {
		t.Fatalf("stuckAfter without timeout = %s", d)
	}
}
