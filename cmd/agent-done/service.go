package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Service struct {
	mu             sync.Mutex // Serializes state transitions, observations and publication.
	desktopMu      sync.Mutex
	desktop        desktopBackend
	state          *State
	previous       []byte
	subscribers    map[chan *Snapshot]struct{}
	remoteUpdates  chan struct{}
	remoteActivity map[string]float64
}

func (s *Service) publish(snapshot *Snapshot) {
	raw, err := json.Marshal(snapshot)
	if err != nil || bytes.Equal(raw, s.previous) {
		return
	}
	s.previous = raw
	for updates := range s.subscribers {
		select {
		case <-updates:
		default:
		}
		updates <- snapshot
	}
}

func (s *Service) observe(observation Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.state.observe(observation); err != nil {
		return err
	}
	snapshot, err := s.state.snapshot()
	if err == nil {
		s.publish(snapshot)
	}
	return err
}

func (s *Service) disconnect(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.disconnect(cause)
	if snapshot, err := s.state.snapshot(); err == nil {
		s.publish(snapshot)
	} else {
		log.Print(err)
	}
}

func (s *Service) identify(ctx context.Context, pid int) (Response, error) {
	if pid <= 1 {
		return Response{}, errors.New("invalid source pid")
	}
	chain := ancestors(pid)
	retryUntil := time.Now().Add(time.Second)
	for {
		s.desktopMu.Lock()
		var err error
		if s.desktop.register != nil {
			err = s.desktop.register(ctx, chain)
		}
		var desktop Observation
		if err == nil {
			desktop, err = s.desktop.snapshot(ctx)
			if err == nil && s.desktop.events == nil {
				err = s.observe(desktop)
			}
		}
		s.desktopMu.Unlock()
		var identity *Identity
		if err == nil {
			identity, err = identifyWindow(desktop, chain)
		}
		if !errors.Is(err, errSourceWindowNotFound) || time.Now().After(retryUntil) {
			return Response{OK: true, Identity: identity}, err
		}
		// An automatically launched terminal can spawn its command before
		// the desktop reports the window. Keep the same ancestry check on every attempt.
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(75 * time.Millisecond):
		}
	}
}

func (s *Service) desktopRequest(ctx context.Context, r Request) (Response, error) {
	if r.Op == "focus" && (r.Backend != s.desktop.name || r.WindowID == nil) {
		return Response{}, errors.New("目标与服务的桌面后端不一致或缺少窗口 ID")
	}
	if r.Op == "identify" {
		return s.identify(ctx, r.PID)
	}
	s.desktopMu.Lock()
	defer s.desktopMu.Unlock()
	desktop, err := s.desktop.snapshot(ctx)
	if err != nil {
		s.disconnect(err)
		return Response{}, err
	}
	result := Response{OK: true}
	switch r.Op {
	case "desktop_status":
		if err := s.observe(desktop); err != nil {
			return Response{}, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		result.Desktop = &desktop
		result.Snapshot, err = s.state.snapshot()
		return result, err
	case "focus":
		requested := false
		result.Requested = &requested
		for _, window := range desktop.Windows {
			if window.ID != *r.WindowID || (r.WindowPID != 0 && (r.WindowPID != window.PID || r.WindowStart != window.Start)) {
				continue
			}
			if err := s.desktop.focus(ctx, window); err != nil {
				return Response{}, err
			}
			requested = true
			fresh, err := s.desktop.snapshot(ctx)
			if err != nil {
				s.disconnect(err)
				return Response{}, err
			}
			return result, s.observe(fresh)
		}
		return result, nil
	}
	return Response{}, errors.New("unknown desktop operation")
}

func (s *Service) notifyRemote() {
	select {
	case s.remoteUpdates <- struct{}{}:
	default:
	}
}

func (s *Service) dispatch(ctx context.Context, r Request) (Response, error) {
	switch r.Op {
	case "identify", "desktop_status", "focus":
		return s.desktopRequest(ctx, r)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Op == "history_changed" {
		if r.Session != "" && s.remoteActivity != nil {
			s.remoteActivity[r.Session] = now()
			s.notifyRemote()
		}
		return Response{OK: true}, nil
	}
	if r.Op != "status" && r.Op != "diagnostics" {
		if err := s.state.apply(r); err != nil {
			return Response{}, err
		}
	}
	snapshot, err := s.state.snapshot()
	if err != nil {
		return Response{}, err
	}
	switch r.Op {
	case "bind", "unbind", "deactivate", "remove", "running", "complete", "interrupt", "title", "request_open", "request_close":
		s.notifyRemote()
		if r.Op == "remove" {
			delete(s.remoteActivity, r.Session)
		}
	}
	s.publish(snapshot)
	result := Response{OK: true, Snapshot: snapshot}
	if r.Op == "diagnostics" {
		subscribers := len(s.subscribers)
		result.Hooks, err = s.state.diagnostics()
		result.Subscribers = &subscribers
		result.ServiceExecutable, _ = os.Executable()
	}
	return result, err
}

func (s *Service) subscribe(ctx context.Context, conn net.Conn, reader *bufio.Reader, send func(Response) error) {
	updates := make(chan *Snapshot, 1)
	s.mu.Lock()
	initial, err := s.state.snapshot()
	if err == nil {
		s.subscribers[updates] = struct{}{}
	}
	s.mu.Unlock()
	if err != nil {
		send(Response{Error: err.Error()})
		return
	}
	defer func() {
		s.mu.Lock()
		delete(s.subscribers, updates)
		s.mu.Unlock()
	}()
	if err := send(Response{OK: true, Snapshot: initial}); err != nil {
		return
	}
	disconnected := make(chan struct{})
	go func() {
		reader.ReadByte()
		close(disconnected)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-disconnected:
			return
		case snapshot := <-updates:
			if err := send(Response{OK: true, Snapshot: snapshot}); err != nil {
				return
			}
		}
	}
}

func (s *Service) client(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()
	send := func(response Response) error {
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return json.NewEncoder(conn).Encode(response)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(conn, 65536)
	raw, err := reader.ReadSlice('\n')
	var r Request
	if err != nil || json.Unmarshal(raw, &r) != nil {
		if !errors.Is(err, io.EOF) {
			send(Response{Error: "invalid request"})
		}
		return
	}
	conn.SetReadDeadline(time.Time{})
	if r.Op == "subscribe" {
		s.subscribe(ctx, conn, reader, send)
		return
	}
	response, err := s.dispatch(ctx, r)
	if err != nil {
		response = Response{Error: err.Error()}
	}
	send(response)
}

func (s *Service) watchDesktop(ctx context.Context) {
	for ctx.Err() == nil {
		var err error
		if s.desktop.events == nil {
			err = s.pollDesktop(ctx)
		} else {
			err = s.desktop.events(ctx, func(observation Observation) {
				if err := s.observe(observation); err != nil {
					log.Print(err)
				}
			})
		}
		if ctx.Err() != nil {
			break
		}
		s.disconnect(err)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Service) prune(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		changed, err := s.state.prune()
		if err != nil {
			log.Print(err)
		} else if snapshot, err := s.state.snapshot(); err == nil {
			if changed {
				s.notifyRemote()
			}
			s.publish(snapshot)
		} else {
			log.Print(err)
		}
		s.mu.Unlock()
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func serve(ctx context.Context) error {
	desktop, err := selectDesktop(envOr("AGENT_DONE_DESKTOP", defaultDesktop()))
	if err != nil {
		return err
	}
	if desktop.close != nil {
		defer desktop.close()
	}
	for _, directory := range []string{stateDir(), runtimeDir()} {
		if err := privateDir(directory); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir(), "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	path := controlSocket()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := openState(filepath.Join(stateDir(), "state.sqlite3"), desktop.name)
	if err != nil {
		return err
	}
	defer state.db.Close()
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	group := sync.WaitGroup{}
	defer group.Wait()
	defer cancel()
	service := &Service{state: state, desktop: desktop, subscribers: make(map[chan *Snapshot]struct{}),
		remoteUpdates: make(chan struct{}, 1), remoteActivity: make(map[string]float64)}
	group.Go(func() { service.watchDesktop(ctx) })
	group.Go(func() { service.prune(ctx) })
	group.Go(func() { service.serveRemote(ctx) })
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		group.Go(func() { service.client(ctx, conn) })
	}
}

func localRequest(r Request, timeout time.Duration) (Response, error) {
	conn, err := net.DialTimeout("unix", controlSocket(), timeout)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(r); err != nil {
		return Response{}, err
	}
	var response Response
	if err := json.NewDecoder(io.LimitReader(conn, 4<<20)).Decode(&response); err != nil {
		return Response{}, err
	}
	if !response.OK {
		return Response{}, errors.New(response.Error)
	}
	return response, nil
}
