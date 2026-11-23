package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func hook() {
	var event Hook
	if json.NewDecoder(io.LimitReader(os.Stdin, 8<<20)).Decode(&event) == nil {
		localRequest(Request{Op: "hook", Event: &event}, 400*time.Millisecond)
	}
	fmt.Println("{}")
}

func statusAndAck(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	r := Request{Op: command}
	asJSON := command == "ack"
	if command == "ack" {
		flags.StringVar(&r.Session, "session", "", "仅清除该会话的提醒")
	} else {
		flags.BoolVar(&asJSON, "json", false, "输出 JSON")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return errors.New("unexpected positional arguments")
	}
	response, err := localRequest(r, time.Second)
	if err != nil {
		return err
	}
	snapshot := response.Snapshot
	if snapshot == nil {
		return errors.New("服务未返回状态")
	}
	if asJSON {
		return printJSON(snapshot)
	}
	fmt.Printf("待处理 %d 个会话\n", snapshot.PendingCount)
	for _, session := range snapshot.Sessions {
		mark := " "
		if session.Pending {
			mark = "*"
		}
		fmt.Printf("%s %s  %s  %s\n", mark, session.ID, session.Phase, sessionTitle(session))
	}
	return nil
}

func doctor() error {
	checks := map[string]any{"version": version, "codex_socket": codexSocket(),
		"codex_available": exists(codexSocket()), "control_socket": controlSocket()}
	response, err := localRequest(Request{Op: "diagnostics"}, time.Second)
	checks["service_available"] = err == nil
	if err == nil && response.Snapshot != nil {
		s := response.Snapshot
		checks["pending_count"], checks["niri_connected"] = s.PendingCount, s.NiriConnected
		checks["desktop_backend"], checks["desktop_connected"], checks["desktop_error"] = s.DesktopBackend, s.DesktopConnected, s.DesktopError
		checks["service_executable"], checks["status_subscribers"] = response.ServiceExecutable, response.Subscribers
		checks["recent_hooks"], checks["experimental"] = response.Hooks, false
	}
	return printJSON(checks)
}

func jump() error {
	response, err := localRequest(Request{Op: "status"}, time.Second)
	if err != nil {
		return err
	}
	if response.Snapshot == nil {
		return errors.New("服务未返回状态")
	}
	sessions := response.Snapshot.Sessions
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Updated == sessions[j].Updated {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].Updated < sessions[j].Updated
	})
	for _, session := range sessions {
		if !session.Pending {
			continue
		}
		views := make([]View, 0, len(session.Views))
		for _, view := range session.Views {
			if !view.Active || view.Backend != response.Snapshot.DesktopBackend || view.WindowID == nil ||
				!sameProcess(view.PID, view.StartTicks) || !sameProcess(view.OwnerPID, view.OwnerStart) {
				continue
			}
			views = append(views, view)
		}
		sort.Slice(views, func(i, j int) bool { return *views[i].WindowID < *views[j].WindowID })
		for _, view := range views {
			result, err := localRequest(Request{Op: "focus", Backend: view.Backend, WindowID: view.WindowID,
				WindowPID: view.WindowPID, WindowStart: view.WindowStart}, 15*time.Second)
			if err != nil {
				status, queryErr := localRequest(Request{Op: "desktop_status"}, 15*time.Second)
				if queryErr != nil || status.Desktop == nil {
					return err
				}
				for _, window := range status.Desktop.Windows {
					if window.ID == *view.WindowID {
						return err
					}
				}
				continue
			}
			if result.Requested != nil && *result.Requested {
				fmt.Printf("已请求聚焦窗口 %d（%s）\n", *view.WindowID, session.ID)
				return nil
			}
		}
	}
	fmt.Println("没有可跳转的待介入会话")
	return nil
}

func sessionTitle(session Session) string {
	if session.Title != "" {
		return session.Title
	}
	if session.CWD != "" {
		return filepath.Base(session.CWD)
	}
	return shorten(session.ID, 8)
}

type WaybarUpdate struct {
	Text    string `json:"text"`
	Tooltip string `json:"tooltip"`
	Class   any    `json:"class"`
}

func renderWaybar(s *Snapshot) WaybarUpdate {
	reasons := map[string]string{"done": "已完成", "approval": "待授权", "question": "待回答", "error": "执行失败"}
	header := "没有待处理会话"
	class := []string{"idle"}
	if s.PendingCount > 0 {
		header = fmt.Sprintf("待处理 %d 个 Codex 会话", s.PendingCount)
		class[0] = "pending"
	}
	lines := []string{header}
	for _, session := range s.Sessions {
		if !session.Pending {
			continue
		}
		title := shorten(strings.Join(strings.Fields(sessionTitle(session)), " "), 100)
		reason := reasons[session.Reason]
		if reason == "" {
			reason = "待查看"
		}
		lines = append(lines, title+" — "+reason)
		for _, view := range session.Views {
			location := "位置待更新"
			if view.WindowID == nil {
				location = "无关联窗口"
			}
			if position := view.Position; position != nil {
				output := position.Output
				if output == "" {
					output = "未知显示器"
				}
				workspace := fmt.Sprint(position.Workspace)
				if position.Workspace == nil || workspace == "0" || workspace == "" {
					workspace = "?"
				}
				location = output + " · 工作区 " + workspace
				if position.Floating {
					location += " · 浮动窗口"
				} else if position.Row != nil && position.Column != nil {
					location += fmt.Sprintf(" · 第 %d 行第 %d 列", *position.Row, *position.Column)
				}
			}
			if !view.Active {
				location += "（终端内的其他会话）"
			}
			lines = append(lines, "  "+location)
		}
	}
	if !s.DesktopConnected {
		lines = append(lines, "niri 连接中，位置和查看状态稍后更新")
		class = append(class, "disconnected")
	}
	// Waybar interprets Pango markup in tooltips.
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return WaybarUpdate{Text: fmt.Sprintf("Codex %d", s.PendingCount), Tooltip: escape.Replace(strings.Join(lines, "\n")), Class: class}
}

func streamSnapshots(ctx context.Context, emit func(WaybarUpdate) error) error {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", controlSocket())
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(Request{Op: "subscribe"}); err != nil {
		return err
	}
	decoder := json.NewDecoder(conn)
	for {
		var response Response
		if err := decoder.Decode(&response); err != nil {
			return err
		}
		if !response.OK || response.Snapshot == nil {
			return errors.New("提醒服务未返回状态")
		}
		if err := emit(renderWaybar(response.Snapshot)); err != nil {
			return err
		}
	}
}

func waybar(ctx context.Context) error {
	emit := json.NewEncoder(os.Stdout).Encode
	for ctx.Err() == nil {
		if err := ensureService(ctx); err == nil {
			if err := streamSnapshots(ctx, func(u WaybarUpdate) error { return emit(u) }); errors.Is(err, syscall.EPIPE) {
				return nil
			}
		}
		if ctx.Err() != nil {
			break
		}
		if err := emit(WaybarUpdate{Text: "Codex ?", Tooltip: "提醒服务连接中", Class: "disconnected"}); err != nil {
			if errors.Is(err, syscall.EPIPE) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func usage() {
	fmt.Println("用法: agent-done {codex|serve|waybar|jump|status|ack|doctor|install|remote}")
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if args[0] == "hook" {
		hook()
		return
	}
	if args[0] == "--help" || args[0] == "-h" {
		usage()
		return
	}
	if args[0] == "--version" {
		fmt.Println(version)
		return
	}
	switch args[0] {
	case "serve", "waybar", "jump", "doctor", "install":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			fmt.Println("用法: agent-done " + args[0])
			return
		}
		if len(args) != 1 {
			fmt.Fprintln(os.Stderr, "不支持的参数："+strings.Join(args[1:], " "))
			os.Exit(2)
		}
	}
	log.SetPrefix("agent-done: ")
	code := 0
	var err error
	switch args[0] {
	case "remote":
		err = runRemote(args[1:])
	case "codex":
		args = args[1:]
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		code, err = launchCodex(args)
	case "serve", "waybar":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		if args[0] == "serve" {
			err = serve(ctx)
		} else {
			err = waybar(ctx)
		}
		cancel()
	case "jump":
		err = jump()
	case "doctor":
		err = doctor()
	case "status", "ack":
		err = statusAndAck(args[0], args[1:])
	case "install":
		if runtime.GOOS != "linux" {
			err = errors.New("install 用于 Linux/Waybar。macOS 请构建 agent-done 并启动 serve")
			break
		}
		var result map[string]string
		result, err = install(homeDir(), executable(), stateDir())
		if err == nil {
			err = printJSON(result)
		}
	default:
		usage()
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-done：%v\n", err)
		code = 1
	}
	os.Exit(code)
}
