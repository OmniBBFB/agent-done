package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

var hookEvents = []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "Interrupt"}

func executable() string {
	path, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return path
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func hookCommand() string { return shellQuote(executable()) + " hook" }

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func str(value any) string {
	result, _ := value.(string)
	return result
}

func preservingHooks(config map[string]any, command string) map[string]any {
	if config == nil {
		config = map[string]any{}
	}
	for event, nested := range object(config["hooks"]) {
		existing, _ := config["hooks."+event].([]any)
		groups, _ := nested.([]any)
		config["hooks."+event] = append(existing, groups...)
	}
	delete(config, "hooks")
	for _, event := range hookEvents {
		key := "hooks." + event
		groups, _ := config[key].([]any)
		found := false
		for _, group := range groups {
			hooks, _ := object(group)["hooks"].([]any)
			for _, hook := range hooks {
				if object(hook)["command"] == command {
					found = true
				}
			}
		}
		if !found {
			config[key] = append(groups, map[string]any{"hooks": []any{
				map[string]any{"type": "command", "command": command, "timeout": 3},
			}})
		}
	}
	return config
}

func trackableThread(method string) bool {
	return method == "thread/start" || method == "thread/resume" || method == "thread/fork"
}

func requestKind(method string) string {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval",
		"item/permissions/requestApproval", "execCommandApproval", "applyPatchApproval":
		return "approval"
	case "item/tool/requestUserInput", "tool/requestUserInput", "mcpServer/elicitation/request":
		return "question"
	}
	return ""
}

func rpcKey(id any) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

type RPCContext struct {
	method   string
	session  string
	internal bool
}

type Observer struct {
	mu             sync.Mutex
	connection     string
	command        string
	identity       func() Identity
	emit           func(Request)
	clientRequests map[string]RPCContext
	serverRequests map[string]string
	sessions       map[string]bool
}

func newObserver(identity func() Identity, emit func(Request)) *Observer {
	return &Observer{connection: connectionID(), command: hookCommand(), identity: identity, emit: emit,
		clientRequests: map[string]RPCContext{}, serverRequests: map[string]string{}, sessions: map[string]bool{}}
}

func (o *Observer) client(message map[string]any) bool {
	method, params := str(message["method"]), object(message["params"])
	id, hasID := message["id"]
	key := rpcKey(id)
	source := str(params["threadSource"])
	internal := params["ephemeral"] == true || source == "thread_title" || source == "subagent" || source == "subAgent"
	if method != "" && hasID {
		o.clientRequests[key] = RPCContext{method: method, session: str(params["threadId"]), internal: internal}
	}
	if method == "" && hasID {
		if sid, ok := o.serverRequests[key]; ok {
			delete(o.serverRequests, key)
			o.emit(Request{Op: "request_close", Session: sid, Key: o.connection + ":" + key})
		}
	}
	if !trackableThread(method) || internal {
		return false
	}
	if params == nil {
		params = make(map[string]any)
	}
	params["config"] = preservingHooks(object(params["config"]), o.command)
	message["params"] = params
	return true
}

func (o *Observer) server(message map[string]any) {
	method, params := str(message["method"]), object(message["params"])
	sid := str(params["threadId"])
	if sid == "" {
		sid = str(params["thread_id"])
	}
	turn := str(params["turnId"])
	if turn == "" {
		turn = str(params["turn_id"])
	}
	id, hasID := message["id"]
	if hasID && method == "" {
		key := rpcKey(id)
		context := o.clientRequests[key]
		delete(o.clientRequests, key)
		thread := object(object(message["result"])["thread"])
		if threadID := str(thread["id"]); threadID != "" && trackableThread(context.method) && !context.internal {
			identity := o.identity()
			o.sessions[threadID] = true
			o.emit(Request{Op: "bind", Session: threadID, Connection: o.connection, Identity: &identity,
				Title: str(thread["name"]), CWD: str(thread["cwd"])})
		} else if context.method == "thread/unsubscribe" && o.sessions[context.session] && message["error"] == nil {
			o.emit(Request{Op: "deactivate", Session: context.session, Connection: o.connection})
		}
		return
	}
	if !o.sessions[sid] {
		return
	}
	event := Request{Session: sid, Connection: o.connection, Turn: turn}
	switch {
	case method == "turn/started" || method == "turn/completed":
		details := object(params["turn"])
		event.Turn = str(details["id"])
		event.Op = "running"
		if method == "turn/completed" {
			event.Op, event.Status = "complete", str(details["status"])
		}
	case requestKind(method) != "" && hasID:
		key := rpcKey(id)
		o.serverRequests[key] = sid
		event.Op, event.Kind, event.Key = "request_open", requestKind(method), o.connection+":"+key
	case method == "serverRequest/resolved":
		key := rpcKey(params["requestId"])
		if owner, ok := o.serverRequests[key]; ok {
			event.Session = owner
		}
		delete(o.serverRequests, key)
		event.Op, event.Key = "request_close", o.connection+":"+key
	case method == "item/completed":
		item := object(params["item"])
		questions, _ := item["questions"].([]any)
		itemID := str(item["id"])
		if str(item["type"]) != "agentMessage" || str(item["delivery"]) != "async" || len(questions) == 0 || itemID == "" {
			return
		}
		event.Op, event.Kind, event.Key = "request_open", "question", o.connection+":async-question:"+itemID
	case method == "thread/name/updated":
		event.Op, event.Title = "title", str(params["threadName"])
		if event.Title == "" {
			event.Title = str(params["name"])
		}
	case method == "thread/archived" || method == "thread/deleted" || method == "thread/closed":
		event.Op = "remove"
		delete(o.sessions, sid)
	default:
		return
	}
	o.emit(event)
}

func (o *Observer) observe(raw []byte, fromClient bool) []byte {
	var message map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&message) != nil || message == nil || !json.Valid(raw) {
		return raw
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !fromClient {
		o.server(message)
		return raw
	}
	if o.client(message) {
		if changed, err := json.Marshal(message); err == nil {
			return changed
		}
	}
	return raw
}

func proxyConnection(ctx context.Context, client *websocket.Conn, observer *Observer) {
	defer client.Close()
	defer observer.emit(Request{Op: "unbind", Connection: observer.connection})
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", codexSocket())
		},
	}
	upstream, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		log.Printf("Codex daemon connection: %v", err)
		return
	}
	defer upstream.Close()
	closeBoth := func() { client.Close(); upstream.Close() }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	var group sync.WaitGroup
	relay := func(src, dst *websocket.Conn, fromClient bool) {
		defer closeBoth()
		for {
			frame, raw, err := src.ReadMessage()
			if err != nil {
				return
			}
			if err := dst.WriteMessage(frame, observer.observe(raw, fromClient)); err != nil {
				return
			}
		}
	}
	group.Go(func() { relay(client, upstream, true) })
	group.Go(func() { relay(upstream, client, false) })
	group.Wait()
}

func ensureService() error {
	if _, err := localRequest(Request{Op: "status"}, 300*time.Millisecond); err == nil {
		return nil
	}
	unit := filepath.Join(homeDir(), ".config", "systemd", "user", "agent-done.service")
	if exists(unit) && os.Getenv("AGENT_DONE_STATE_DIR") == "" && os.Getenv("AGENT_DONE_RUNTIME_DIR") == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exec.CommandContext(ctx, "systemctl", "--user", "start", "agent-done.service").Run()
	} else {
		if err := privateDir(stateDir()); err != nil {
			return err
		}
		output, err := os.OpenFile(filepath.Join(stateDir(), "service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer output.Close()
		cmd := exec.Command(executable(), "serve")
		cmd.Stdout, cmd.Stderr = output, output
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait()
	}
	for range 50 {
		if _, err := localRequest(Request{Op: "status"}, 300*time.Millisecond); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("提醒服务未能启动")
}

func codexArgs(args []string, remote, cwd, command string) []string {
	result := []string{"--remote", "unix://" + remote}
	for _, event := range hookEvents {
		result = append(result, "-c", "hooks."+event+"=[{hooks=[{type=\"command\",command="+strconv.Quote(command)+",timeout=3}]}]")
	}
	explicitCWD := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--cd" || strings.HasPrefix(arg, "--cd=") || strings.HasPrefix(arg, "-C") {
			explicitCWD = true
			break
		}
	}
	if !explicitCWD {
		result = append(result, "--cd", cwd)
	}
	return append(result, args...)
}

func runChild(cmd *exec.Cmd, onStart func(int)) (int, error) {
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	if onStart != nil {
		onStart(cmd.Process.Pid)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		select {
		case sig := <-sigs:
			// The terminal sends SIGINT to both members of the foreground group.
			if sig != os.Interrupt {
				cmd.Process.Signal(syscall.SIGTERM)
			}
		case err := <-done:
			if err == nil {
				return 0, nil
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				if result, ok := exit.Sys().(syscall.WaitStatus); ok && result.Signaled() {
					return 128 + int(result.Signal()), nil
				}
				return exit.ExitCode(), nil
			}
			return 1, err
		}
	}
}

func launchCodex(args []string) (int, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return 1, errors.New("PATH 中没有 codex")
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		switch arg {
		case "--help", "-h", "--version", "-V":
			return runChild(exec.Command(path, args...), nil)
		case "--no-daemon", "--remote":
			return 1, errors.New("此入口管理本机 daemon 连接，请移除 --remote / --no-daemon")
		}
		if strings.HasPrefix(arg, "--remote=") {
			return 1, errors.New("此入口管理本机 daemon 连接，请移除 --remote / --no-daemon")
		}
	}
	owner := os.Getpid()
	var response Response
	if err = ensureService(); err == nil {
		response, err = localRequest(Request{Op: "identify", PID: owner}, 15*time.Second)
	}
	if err == nil && response.Identity == nil {
		err = errors.New("提醒服务未返回源窗口")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-done：提醒暂不可用（%v），继续启动 Codex。\n", err)
		return runChild(exec.Command(path, args...), nil)
	}
	if !exists(codexSocket()) {
		cmd := exec.Command(path, "app-server", "daemon", "start")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return 1, fmt.Errorf("无法启动 Codex daemon: %w", err)
		}
	}
	if err := privateDir(runtimeDir()); err != nil {
		return 1, err
	}
	dir, err := os.MkdirTemp(runtimeDir(), "codex-")
	if err != nil {
		return 1, err
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "proxy.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return 1, err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return 1, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var warn sync.Once
	emit := func(r Request) {
		if _, err := localRequest(r, 500*time.Millisecond); err != nil {
			warn.Do(func() { log.Printf("attention service unavailable; Codex traffic continues: %v", err) })
		}
	}
	var childPID int
	started := make(chan struct{})
	ownerStart := startTicks(owner)
	identity := func() Identity {
		id := *response.Identity
		id.PID = codexPID(childPID)
		id.StartTicks = startTicks(id.PID)
		id.OwnerPID, id.OwnerStart = owner, ownerStart
		return id
	}
	upgrader := websocket.Upgrader{}
	var handlerMu sync.Mutex
	var handlers sync.WaitGroup
	shutdown := false
	server := http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerMu.Lock()
		if shutdown {
			handlerMu.Unlock()
			http.Error(w, "proxy closed", http.StatusServiceUnavailable)
			return
		}
		handlers.Add(1)
		handlerMu.Unlock()
		defer handlers.Done()
		select {
		case <-ctx.Done():
			return
		case <-started:
		}
		client, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			proxyConnection(ctx, client, newObserver(identity, emit))
		}
	})}
	go server.Serve(listener)
	cwd, err := os.Getwd()
	if err == nil {
		code, childErr := runChild(exec.Command(path, codexArgs(args, socket, cwd, hookCommand())...), func(pid int) { childPID = pid; close(started) })
		handlerMu.Lock()
		shutdown = true
		handlerMu.Unlock()
		cancel()
		server.Close()
		handlers.Wait()
		return code, childErr
	}
	server.Close()
	return 1, err
}
