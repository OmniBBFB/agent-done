package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestObserverKeepsPerConnectionRequestOwnership(t *testing.T) {
	var first, second []Request
	a := newObserver(func() Identity { return Identity{} }, func(r Request) { first = append(first, r) })
	b := newObserver(func() Identity { return Identity{} }, func(r Request) { second = append(second, r) })
	bind := func(o *Observer, sid string) {
		t.Helper()
		request := []byte(`{"id":1,"method":"thread/resume","params":{"config":{"model":"model","hooks":{"Stop":[{"hooks":[{"type":"command","command":"existing"}]}]}}}}`)
		result := o.observe(request, true)
		var parsed map[string]any
		if err := json.Unmarshal(result, &parsed); err != nil {
			t.Fatal(err)
		}
		config := object(object(parsed["params"])["config"])
		groups, ok := config["hooks.Stop"].([]any)
		if !ok || len(groups) != 2 || config["model"] != "model" {
			t.Fatalf("configuration was not merged: %s", result)
		}
		wire := []byte(`{"id":1,"result":{"thread":{"id":"` + sid + `"}}}`)
		if !bytes.Equal(wire, o.observe(wire, false)) {
			t.Fatal("daemon response changed")
		}
	}
	bind(a, "a")
	bind(b, "b")
	a.observe([]byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"a","turnId":"t"}}`), false)
	b.observe([]byte(`{"id":7,"method":"item/tool/requestUserInput","params":{"threadId":"b","turnId":"t"}}`), false)
	if first[1].Key == second[1].Key || first[1].Kind != "approval" || second[1].Kind != "question" {
		t.Fatalf("request identity leaked across connections: %+v %+v", first, second)
	}
	a.observe([]byte(`{"id":7,"result":{"decision":"decline"}}`), true)
	if first[2].Key != first[1].Key || first[2].Op != "request_close" || len(second) != 2 {
		t.Fatalf("unexpected request closure: %+v %+v", first, second)
	}
	b.observe([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"b","requestId":7}}`), false)
	b.observe([]byte(`{"method":"item/completed","params":{"threadId":"b","turnId":"t","item":{"type":"agentMessage","id":"next","delivery":"async","questions":[{"text":"choose"}]}}}`), false)
	if len(second) != 5 || second[2].Key != second[1].Key || second[3].Op != "history_changed" || second[4].Kind != "question" {
		t.Fatalf("question lifecycle: %+v", second)
	}
}

func TestCodexArgsPreservesExplicitCWD(t *testing.T) {
	for _, args := range [][]string{{"resume", "--last"}, {"-C/other"}, {"--cd=/other"}, {"resume", "--cd", "/other"}, {"--", "--cd"}} {
		wantDefault := len(args) == 2 && (args[0] == "resume" && args[1] == "--last" || args[0] == "--")
		result := codexArgs(args, "/tmp/proxy.sock", "/project", "agent-done hook")
		gotDefault := false
		for _, arg := range result {
			gotDefault = gotDefault || arg == "/project"
		}
		if gotDefault != wantDefault {
			t.Fatalf("unexpected cwd arguments: %v", result)
		}
	}
}
