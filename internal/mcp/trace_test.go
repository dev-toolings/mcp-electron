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
}
