package main

type Identity struct {
	Backend     string `json:"backend"`
	WindowID    *int64 `json:"window_id"`
	WindowPID   int    `json:"window_pid"`
	WindowStart string `json:"window_start"`
	PID         int    `json:"pid"`
	StartTicks  string `json:"start_ticks"`
	OwnerPID    int    `json:"owner_pid"`
	OwnerStart  string `json:"owner_start_ticks"`
}

type Position struct {
	Output    string `json:"output"`
	Workspace any    `json:"workspace"`
	Row       *int   `json:"row"`
	Column    *int   `json:"column"`
	Floating  bool   `json:"floating"`
}

type Window struct {
	ID       int64     `json:"id"`
	PID      int       `json:"pid"`
	Start    string    `json:"start"`
	Position *Position `json:"position"`
}

type Observation struct {
	Backend string   `json:"backend"`
	Windows []Window `json:"windows"`
	Focused *int64   `json:"focused"`
}

type View struct {
	Identity
	Connection string    `json:"connection"`
	Session    string    `json:"session"`
	Active     bool      `json:"active"`
	Position   *Position `json:"position"`
}

type Session struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	CWD     string  `json:"cwd"`
	Phase   string  `json:"phase"`
	TurnID  string  `json:"turn_id"`
	Pending bool    `json:"pending"`
	Reason  string  `json:"reason"`
	Updated float64 `json:"updated"`
	Views   []View  `json:"views"`
}

type Snapshot struct {
	Sessions         []Session `json:"sessions"`
	PendingCount     int       `json:"pending_count"`
	DesktopBackend   string    `json:"desktop_backend"`
	DesktopConnected bool      `json:"desktop_connected"`
	DesktopError     string    `json:"desktop_error"`
	NiriConnected    bool      `json:"niri_connected"`
	FocusedWindow    *int64    `json:"focused_window"`
}

type Hook struct {
	Session string `json:"session_id"`
	Turn    string `json:"turn_id"`
	Name    string `json:"hook_event_name"`
	Source  string `json:"source,omitempty"`
}

type HookRecord struct {
	Session string  `json:"session"`
	Event   string  `json:"event"`
	TurnID  string  `json:"turn_id"`
	Created float64 `json:"created"`
}

type Request struct {
	Op          string    `json:"op"`
	Session     string    `json:"session,omitempty"`
	Connection  string    `json:"connection,omitempty"`
	Turn        string    `json:"turn,omitempty"`
	Key         string    `json:"key,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Status      string    `json:"status,omitempty"`
	Title       string    `json:"title,omitempty"`
	CWD         string    `json:"cwd,omitempty"`
	Identity    *Identity `json:"identity,omitempty"`
	Event       *Hook     `json:"event,omitempty"`
	PID         int       `json:"pid,omitempty"`
	Backend     string    `json:"backend,omitempty"`
	WindowID    *int64    `json:"window_id,omitempty"`
	WindowPID   int       `json:"window_pid,omitempty"`
	WindowStart string    `json:"window_start,omitempty"`
}

type Response struct {
	OK                bool          `json:"ok"`
	Error             string        `json:"error,omitempty"`
	Snapshot          *Snapshot     `json:"snapshot,omitempty"`
	Desktop           *Observation  `json:"desktop,omitempty"`
	Identity          *Identity     `json:"identity,omitempty"`
	Requested         *bool         `json:"requested,omitempty"`
	Hooks             *[]HookRecord `json:"hooks,omitempty"`
	Subscribers       *int          `json:"subscribers,omitempty"`
	ServiceExecutable string        `json:"service_executable,omitempty"`
}
