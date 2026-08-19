package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabioconcina/claumon/internal/parser"
	"github.com/fabioconcina/claumon/internal/pricing"
)

// fixture builds a claude dir containing one session transcript, and returns
// the dir plus the session id.
func fixture(t *testing.T, lines []map[string]any) (string, string) {
	t.Helper()
	claudeDir := t.TempDir()
	const sessionID = "sess-1"
	projDir := filepath.Join(claudeDir, "projects", "-home-someone-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, b...)
		body = append(body, '\n')
	}
	if err := os.WriteFile(filepath.Join(projDir, sessionID+".jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return claudeDir, sessionID
}

func at(sec int) string {
	return time.Date(2026, 8, 18, 12, 0, sec, 0, time.UTC).Format(time.RFC3339)
}

func userMsg(ts string, content any) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": ts,
		"message": map[string]any{"role": "user", "content": content},
	}
}

func assistantMsg(ts string, content []any, usage map[string]any) map[string]any {
	m := map[string]any{"role": "assistant", "model": "claude-sonnet-4-6", "content": content}
	if usage != nil {
		m["usage"] = usage
	}
	return map[string]any{"type": "assistant", "timestamp": ts, "message": m}
}

func toolUse(id, name string, input map[string]any) any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(id string, isErr bool) any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isErr, "content": "ok"}
}

func TestPromptsAndRepliesAppearInOrder(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		userMsg(at(0), "fix the build"),
		assistantMsg(at(2), []any{map[string]any{"type": "text", "text": "On it."}}, nil),
	})
	tl, err := Build(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Events) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(tl.Events), tl.Events)
	}
	if tl.Events[0].Kind != KindPrompt || tl.Events[0].Detail != "fix the build" {
		t.Fatalf("first event: %+v", tl.Events[0])
	}
	if tl.Events[1].Kind != KindMessage || tl.Events[1].Title != "Claude" {
		t.Fatalf("second event: %+v", tl.Events[1])
	}
	if tl.Events[0].Seq != 0 || tl.Events[1].Seq != 1 {
		t.Fatal("events must be numbered in order")
	}
}

func TestToolDurationComesFromItsResult(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Bash", map[string]any{"command": "ls -la"})}, nil),
		userMsg(at(3), []any{toolResult("t1", false)}),
	})
	tl, err := Build(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	ev := tl.Events[0]
	if ev.Kind != KindTool || ev.Title != "Bash" {
		t.Fatalf("event: %+v", ev)
	}
	if ev.DurationMS != 3000 {
		t.Fatalf("duration = %dms, want 3000", ev.DurationMS)
	}
	if ev.Detail != "ls -la" {
		t.Fatalf("detail = %q, want the command", ev.Detail)
	}
	if tl.Totals.ToolMS != 3000 {
		t.Fatalf("total tool ms = %d", tl.Totals.ToolMS)
	}
}

func TestAToolWhoseResultNeverArrivedHasNoDuration(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Bash", map[string]any{"command": "sleep 999"})}, nil),
	})
	tl, _ := Build(dir, id)
	if tl.Events[0].DurationMS != 0 {
		t.Fatalf("duration = %d, want 0 rather than a guess", tl.Events[0].DurationMS)
	}
}

func TestErroredToolIsMarkedAndCounted(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Read", map[string]any{"file_path": "/nope"})}, nil),
		userMsg(at(1), []any{toolResult("t1", true)}),
	})
	tl, _ := Build(dir, id)
	if !tl.Events[0].IsError {
		t.Fatal("tool result flagged is_error must mark the event")
	}
	if tl.Totals.Errors != 1 {
		t.Fatalf("errors = %d, want 1", tl.Totals.Errors)
	}
}

func TestParallelToolsCloseAgainstTheirOwnResults(t *testing.T) {
	// Two calls in one message, results arriving out of order in a later one.
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{
			toolUse("t1", "Read", map[string]any{"file_path": "/a"}),
			toolUse("t2", "Grep", map[string]any{"pattern": "needle"}),
		}, nil),
		userMsg(at(5), []any{toolResult("t2", false)}),
		userMsg(at(9), []any{toolResult("t1", true)}),
	})
	tl, _ := Build(dir, id)
	var read, grep Event
	for _, e := range tl.Events {
		switch e.Title {
		case "Read":
			read = e
		case "Grep":
			grep = e
		}
	}
	if read.DurationMS != 9000 || !read.IsError {
		t.Fatalf("Read: %+v", read)
	}
	if grep.DurationMS != 5000 || grep.IsError {
		t.Fatalf("Grep: %+v", grep)
	}
}

func TestUsageIsChargedOncePerAssistantMessage(t *testing.T) {
	usage := map[string]any{"input_tokens": 100, "output_tokens": 50}
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{
			map[string]any{"type": "text", "text": "Running two things."},
			toolUse("t1", "Read", map[string]any{"file_path": "/a"}),
			toolUse("t2", "Read", map[string]any{"file_path": "/b"}),
		}, usage),
	})
	tl, _ := Build(dir, id)
	charged := 0
	for _, e := range tl.Events {
		if e.TokensIn > 0 || e.TokensOut > 0 {
			charged++
		}
	}
	if charged != 1 {
		t.Fatalf("%d events carry usage; one API response must be charged once", charged)
	}
}

func TestStringContentIsHandled(t *testing.T) {
	// Older transcripts put user content in a plain string.
	dir, id := fixture(t, []map[string]any{userMsg(at(0), "plain string content")})
	tl, _ := Build(dir, id)
	if len(tl.Events) != 1 || tl.Events[0].Detail != "plain string content" {
		t.Fatalf("events: %+v", tl.Events)
	}
}

func TestLongDetailIsTruncatedOnARuneBoundary(t *testing.T) {
	long := ""
	for i := 0; i < 60; i++ {
		long += "café "
	}
	dir, id := fixture(t, []map[string]any{userMsg(at(0), long)})
	tl, _ := Build(dir, id)
	got := tl.Events[0].Detail
	if len(got) > 140 {
		t.Fatalf("detail not truncated: %d bytes", len(got))
	}
	if !utf8Valid(got) {
		t.Fatalf("truncation split a multi-byte character: %q", got)
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	dir, id := fixture(t, []map[string]any{userMsg(at(0), "one")})
	path := filepath.Join(dir, "projects", "-home-someone-proj", id+".jsonl")
	body, _ := os.ReadFile(path)
	body = append([]byte("{not json\n"), body...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	tl, err := Build(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Events) != 1 {
		t.Fatalf("a bad line must not lose the good ones: %+v", tl.Events)
	}
}

func TestMissingSessionIsNotFound(t *testing.T) {
	if _, err := Build(t.TempDir(), "nope"); err == nil {
		t.Fatal("expected an error for a session that does not exist")
	}
}

func TestAgentIDsThatCouldEscapeTheDirectoryAreRejected(t *testing.T) {
	for _, bad := range []string{
		"../../../etc/passwd", "agent-../../x", "agent-zz", "notagent-abc", "",
	} {
		if safeAgentID(bad) {
			t.Errorf("safeAgentID(%q) = true, want false", bad)
		}
	}
	if !safeAgentID("agent-a1bb4b633ebd867a8") {
		t.Error("a real agent id must be accepted")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// writeAgent adds a spawned subagent to the fixture session.
func writeAgent(t *testing.T, claudeDir, sessionID, agentID string, meta agentMeta, lines []map[string]any) {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", "-home-someone-proj", sessionID, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body []byte
	for _, l := range lines {
		b, _ := json.Marshal(l)
		body = append(append(body, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, agentID+".jsonl"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	mb, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, agentID+".meta.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
}

func agentWork(startSec int) []map[string]any {
	return []map[string]any{
		userMsg(at(startSec), "go do the thing"),
		assistantMsg(at(startSec+1), []any{toolUse("a1", "Grep", map[string]any{"pattern": "x"})},
			map[string]any{"input_tokens": 10, "output_tokens": 5}),
		userMsg(at(startSec+4), []any{toolResult("a1", false)}),
	}
}

func TestSubagentIsFoldedIntoTheTaskThatSpawnedIt(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{
			toolUse("toolu_A", "Task", map[string]any{"description": "search the repo"}),
		}, nil),
		userMsg(at(30), []any{toolResult("toolu_A", false)}),
	})
	writeAgent(t, dir, id, "agent-abc123", agentMeta{
		AgentType: "Explore", Description: "search the repo", ToolUseID: "toolu_A", SpawnDepth: 1,
	}, agentWork(1))

	tl, err := Build(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	var sub *Event
	for i := range tl.Events {
		if tl.Events[i].Kind == KindSubagent {
			sub = &tl.Events[i]
		}
	}
	if sub == nil {
		t.Fatalf("no subagent event: %+v", tl.Events)
	}
	if sub.Title != "Explore" {
		t.Fatalf("title = %q, want the agent type", sub.Title)
	}
	if sub.Agent.AgentID != "agent-abc123" || sub.Agent.ToolCalls != 1 {
		t.Fatalf("agent ref: %+v", sub.Agent)
	}
	if tl.Totals.Subagents != 1 {
		t.Fatalf("subagents = %d", tl.Totals.Subagents)
	}
	// The Task call is now a subagent event, not a plain tool call.
	for _, e := range tl.Events {
		if e.Kind == KindTool && e.Title == "Task" {
			t.Fatal("the Task call should have become the subagent event")
		}
	}
}

func TestSubagentCostRollsIntoTheSessionTotal(t *testing.T) {
	// Cost needs the pricing table; without it costFor is a no-op and this
	// would pass or fail for the wrong reason.
	parser.SetPricingTable(pricing.Load(nil))

	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("toolu_A", "Task", map[string]any{"description": "d"})}, nil),
	})
	writeAgent(t, dir, id, "agent-abc123",
		agentMeta{AgentType: "Explore", ToolUseID: "toolu_A"}, agentWork(1))

	tl, _ := Build(dir, id)
	if tl.Totals.CostUSD <= 0 {
		t.Fatal("a subagent's spend appears nowhere in the parent transcript and must be rolled in")
	}
}

func TestAgentsPairWithTheirOwnTaskCallNotTheNearestOne(t *testing.T) {
	// Two Task calls; the agents are discovered in whatever order the
	// directory yields, so only the toolUseId keeps them straight.
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("toolu_first", "Task", map[string]any{"description": "first"})}, nil),
		assistantMsg(at(10), []any{toolUse("toolu_second", "Task", map[string]any{"description": "second"})}, nil),
	})
	// Deliberately reversed: the agent that started later is written against
	// the earlier Task call's id.
	writeAgent(t, dir, id, "agent-aaa111",
		agentMeta{AgentType: "Explore", Description: "second task", ToolUseID: "toolu_second"}, agentWork(20))
	writeAgent(t, dir, id, "agent-bbb222",
		agentMeta{AgentType: "Plan", Description: "first task", ToolUseID: "toolu_first"}, agentWork(1))

	tl, _ := Build(dir, id)
	var subs []Event
	for _, e := range tl.Events {
		if e.Kind == KindSubagent {
			subs = append(subs, e)
		}
	}
	if len(subs) != 2 {
		t.Fatalf("want 2 subagents, got %d", len(subs))
	}
	// The first Task call (t=0) must carry the agent declaring toolu_first.
	if subs[0].Title != "Plan" || subs[0].Agent.AgentID != "agent-bbb222" {
		t.Fatalf("first Task paired with the wrong agent: %+v", subs[0])
	}
	if subs[1].Title != "Explore" || subs[1].Agent.AgentID != "agent-aaa111" {
		t.Fatalf("second Task paired with the wrong agent: %+v", subs[1])
	}
}

func TestAgentWithNoMetaStillAppears(t *testing.T) {
	dir, id := fixture(t, []map[string]any{userMsg(at(0), "hi")})
	// No Task call in the parent at all, and no toolUseId to pair on.
	writeAgent(t, dir, id, "agent-ccc333", agentMeta{}, agentWork(1))

	tl, _ := Build(dir, id)
	found := false
	for _, e := range tl.Events {
		if e.Kind == KindSubagent && e.Agent.AgentID == "agent-ccc333" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unpairable agent must still be shown, not dropped: %+v", tl.Events)
	}
}

func TestBuildAgentReturnsTheAgentsOwnEvents(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("toolu_A", "Task", map[string]any{"description": "d"})}, nil),
	})
	writeAgent(t, dir, id, "agent-abc123",
		agentMeta{AgentType: "Explore", ToolUseID: "toolu_A"}, agentWork(1))

	tl, err := BuildAgent(dir, id, "agent-abc123")
	if err != nil {
		t.Fatal(err)
	}
	if tl.AgentID != "agent-abc123" {
		t.Fatalf("agent id = %q", tl.AgentID)
	}
	if tl.Totals.ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want the agent's own", tl.Totals.ToolCalls)
	}
}

func TestBuildAgentRejectsATraversingID(t *testing.T) {
	dir, id := fixture(t, []map[string]any{userMsg(at(0), "hi")})
	if _, err := BuildAgent(dir, id, "../../../../etc/passwd"); err == nil {
		t.Fatal("a traversing agent id must be refused")
	}
}

func TestSessionContextIsReadFromTheTranscript(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		{"type": "user", "timestamp": at(0), "cwd": "/home/me/Projects/widget",
			"gitBranch": "feature/x",
			"message":   map[string]any{"role": "user", "content": "hi"}},
	})
	tl, err := Build(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	if tl.Cwd != "/home/me/Projects/widget" {
		t.Fatalf("cwd = %q", tl.Cwd)
	}
	if tl.Repo != "widget" {
		t.Fatalf("repo = %q, want the working tree's basename", tl.Repo)
	}
	if tl.GitBranch != "feature/x" {
		t.Fatalf("branch = %q", tl.GitBranch)
	}
}

func TestContextIsReadEvenFromLinesWithNoMessage(t *testing.T) {
	// Claude Code writes bookkeeping lines carrying cwd and branch but no
	// message; skipping them before reading context loses both.
	dir, id := fixture(t, []map[string]any{
		{"type": "mode", "timestamp": at(0), "cwd": "/home/me/repo", "gitBranch": "main"},
		userMsg(at(1), "hello"),
	})
	tl, _ := Build(dir, id)
	if tl.Repo != "repo" || tl.GitBranch != "main" {
		t.Fatalf("repo=%q branch=%q", tl.Repo, tl.GitBranch)
	}
}

func TestDominantModelWinsOnVolumeNotRecency(t *testing.T) {
	events := []Event{
		{Model: "claude-opus-5"},
		{Model: "claude-sonnet-4-6"},
		{Model: "claude-sonnet-4-6"},
		{Model: ""},
	}
	if got := dominantModel(events); got != "claude-sonnet-4-6" {
		t.Fatalf("model = %q, want the one that did most of the work", got)
	}
	if got := dominantModel(nil); got != "" {
		t.Fatalf("no events should yield no model, got %q", got)
	}
}

func TestSessionEndCoversALongCallStartedEarlier(t *testing.T) {
	// A 10-minute tool call started before a later quick message must extend
	// the session's end; taking the last event's timestamp would cut it short.
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Bash", map[string]any{"command": "long"})}, nil),
		userMsg(at(600), []any{toolResult("t1", false)}),
		assistantMsg(at(5), []any{map[string]any{"type": "text", "text": "meanwhile"}}, nil),
	})
	tl, _ := Build(dir, id)
	span := tl.EndedAt.Sub(tl.StartedAt)
	if span < 600*time.Second {
		t.Fatalf("session span = %v, want at least the 10-minute call", span)
	}
}

func TestAgentRefCarriesItsModelAndSpan(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("toolu_A", "Task", map[string]any{"description": "d"})}, nil),
	})
	writeAgent(t, dir, id, "agent-abc123",
		agentMeta{AgentType: "Explore", ToolUseID: "toolu_A"}, agentWork(1))

	tl, _ := Build(dir, id)
	var ref *AgentRef
	for _, e := range tl.Events {
		if e.Agent != nil {
			ref = e.Agent
		}
	}
	if ref == nil {
		t.Fatal("no agent ref")
	}
	if ref.Model != "claude-sonnet-4-6" {
		t.Fatalf("agent model = %q", ref.Model)
	}
	if ref.StartedAt.IsZero() || !ref.EndedAt.After(ref.StartedAt) {
		t.Fatalf("agent span not set: %v..%v", ref.StartedAt, ref.EndedAt)
	}
}

func TestFullTextIsCarriedAlongsideTheSummary(t *testing.T) {
	long := "line one is quite long indeed and keeps going for a while past the summary cut\nline two\nline three"
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Bash", map[string]any{"command": long})}, nil),
	})
	tl, _ := Build(dir, id)
	ev := tl.Events[0]
	if ev.Detail == ev.Full {
		t.Fatal("the summary should be shorter than the full text")
	}
	if !contains(ev.Full, "line three") {
		t.Fatalf("full text truncated too early: %q", ev.Full)
	}
	if contains(ev.Detail, "line two") {
		t.Fatalf("summary should stop at the first line: %q", ev.Detail)
	}
}

func TestToolOutputIsCapturedFromTheResult(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Bash", map[string]any{"command": "ls"})}, nil),
		{"type": "user", "timestamp": at(2), "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "file-a\nfile-b"},
		}}},
	})
	tl, _ := Build(dir, id)
	if tl.Events[0].Output != "file-a\nfile-b" {
		t.Fatalf("output = %q, want the result text", tl.Events[0].Output)
	}
}

func TestToolOutputHandlesBlockShapedResults(t *testing.T) {
	dir, id := fixture(t, []map[string]any{
		assistantMsg(at(0), []any{toolUse("t1", "Read", map[string]any{"file_path": "/a"})}, nil),
		{"type": "user", "timestamp": at(2), "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": []any{
				map[string]any{"type": "text", "text": "contents here"},
			}},
		}}},
	})
	tl, _ := Build(dir, id)
	if tl.Events[0].Output != "contents here" {
		t.Fatalf("output = %q", tl.Events[0].Output)
	}
}

func TestCarriedTextIsCappedOnARuneBoundary(t *testing.T) {
	huge := ""
	for len(huge) < maxFullChars*2 {
		huge += "café ✳ "
	}
	got := clip(huge)
	if len(got) > maxFullChars+32 {
		t.Fatalf("clip returned %d bytes, want ~%d", len(got), maxFullChars)
	}
	if !contains(got, "truncated") {
		t.Fatal("a clipped string should say so")
	}
	for _, r := range got {
		if r == 0xFFFD {
			t.Fatal("clip split a multi-byte character")
		}
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
