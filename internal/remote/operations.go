package remote

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const maxTextBytes = 16 << 10

type ApprovalDetails struct {
	Kind    string `json:"kind"`
	CWD     string `json:"cwd"`
	Reason  string `json:"reason,omitempty"`
	Command string `json:"command,omitempty"`
	Changes string `json:"changes,omitempty"`
}

type Question struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	Options    []string `json:"options"`
	AllowsText bool     `json:"allows_text"`
}

type Interaction struct {
	Session    string           `json:"session_id"`
	ID         string           `json:"interaction_id"`
	Kind       string           `json:"kind"`
	Expires    int64            `json:"expires_at_ms,omitempty"`
	Summary    string           `json:"summary,omitempty"`
	CanApprove *bool            `json:"can_approve,omitempty"`
	Details    *ApprovalDetails `json:"details,omitempty"`
	Questions  []Question       `json:"questions,omitempty"`
}

type InteractionReply struct {
	Kind     string              `json:"kind"`
	Decision string              `json:"decision,omitempty"`
	Answers  map[string][]string `json:"answers,omitempty"`
}

type Controls interface {
	Interactions(context.Context, string, string, int) (Page[Interaction], error)
	Respond(context.Context, string, string, InteractionReply) (string, error)
	InteractionStatus(context.Context, string, string) (string, error)
	Start(context.Context, string, string, string, int64) (string, error)
	AutoApprove(context.Context, string, *bool) (bool, error)
	CreateSession(context.Context) (string, error)
}

type Ledger struct{ db *sql.DB }

func OpenLedger(db *sql.DB) (*Ledger, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS remote_intents (
        peer TEXT NOT NULL,
        kind TEXT NOT NULL,
        session TEXT NOT NULL,
        target TEXT NOT NULL,
        intent TEXT NOT NULL,
        digest BLOB NOT NULL,
        expires INTEGER NOT NULL,
        status TEXT NOT NULL,
        PRIMARY KEY(peer,kind,session,target,intent)
    )`)
	return &Ledger{db: db}, err
}

type operation struct {
	Session string           `json:"session_id"`
	Target  string           `json:"interaction_id"`
	Key     string           `json:"idempotency_key"`
	Expires int64            `json:"expires_at_ms"`
	Text    string           `json:"text"`
	Reply   InteractionReply `json:"response"`
	Enabled *bool            `json:"enabled,omitempty"`
}

func (o operation) identity(peer, kind string) []any {
	return []any{peer, kind, o.Session, o.Target, o.Key}
}

func (h Handler) control(ctx context.Context, peer, method string, params json.RawMessage) (any, *Problem) {
	var op operation
	invalid := &Problem{Code: "invalid_request"}
	if !deviceID.MatchString(peer) || !utf8.Valid(params) ||
		json.Unmarshal(params, &op) != nil || len(op.Session) > 256 {
		return nil, invalid
	}
	op.Key = strings.ToLower(op.Key)
	kind, _, _ := strings.Cut(method, ".")
	statusQuery := strings.HasSuffix(method, ".status")
	if method == "sessions.auto_approve.set" {
		kind = "auto_approve"
	}
	if kind != "auto_approve" && op.Enabled != nil {
		return nil, invalid
	}
	if kind != "sessions" && op.Session == "" {
		return nil, invalid
	}
	resultID, resultName := op.Session, "session_id"
	if kind == "interaction" {
		op.Target = strings.ToLower(op.Target)
		if !uuid.MatchString(op.Target) || (!statusQuery || op.Key != "") && !uuid.MatchString(op.Key) {
			return nil, invalid
		}
		if !statusQuery && !validReply(op.Reply) {
			return nil, invalid
		}
		resultID, resultName = op.Target, "interaction_id"
	} else if kind == "sessions" {
		if op.Session != "" || op.Target != "" || op.Text != "" || !uuid.MatchString(op.Key) || op.Expires <= 0 {
			return nil, invalid
		}
	} else if kind == "auto_approve" {
		if op.Enabled == nil || op.Target != "" || op.Text != "" || !uuid.MatchString(op.Key) || op.Expires <= 0 {
			return nil, invalid
		}
	} else if op.Target != "" || !uuid.MatchString(op.Key) || op.Expires <= 0 ||
		(!statusQuery && (strings.TrimSpace(op.Text) == "" || len(op.Text) > maxTextBytes)) {
		return nil, invalid
	}

	status, err := h.runOperation(ctx, peer, kind, op, statusQuery)
	if err != nil {
		return nil, catalogProblem(err)
	}
	if kind == "sessions" {
		if session, ok := strings.CutPrefix(status, "created:"); ok {
			return map[string]string{"session_id": session, "status": "created"}, nil
		}
		return map[string]string{"status": status}, nil
	}
	if kind == "auto_approve" && (status == "enabled" || status == "disabled") {
		return map[string]any{"session_id": op.Session, "enabled": status == "enabled"}, nil
	}
	return map[string]string{resultName: resultID, "status": status}, nil
}

func validReply(reply InteractionReply) bool {
	if reply.Kind == "approval" {
		return len(reply.Answers) == 0 && (reply.Decision == "allow_once" || reply.Decision == "deny")
	}
	return reply.Kind == "answer" && reply.Decision == "" && len(reply.Answers) > 0
}

func (h Handler) runOperation(ctx context.Context, peer, kind string, op operation, lookup bool) (string, error) {
	args := op.identity(peer, kind)
	var encodedDigest []byte
	var existingExpiry int64
	var existingStatus string
	read := func() error {
		return h.Ledger.db.QueryRowContext(ctx, `SELECT digest,expires,status FROM remote_intents
            WHERE peer=? AND kind=? AND session=? AND target=? AND intent=?`, args...).
			Scan(&encodedDigest, &existingExpiry, &existingStatus)
	}
	err := read()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if lookup {
		if kind == "interaction" {
			status, err := h.Controls.InteractionStatus(ctx, op.Session, op.Target)
			if existingStatus == "unknown" && (err != nil || status == "pending") {
				return "unknown", nil
			}
			return status, err
		}
		if err == nil {
			if existingExpiry != op.Expires {
				return "", &Problem{Code: "conflict"}
			}
			if strings.HasPrefix(existingStatus, "error:") {
				return unreceivedStatus(op.Expires), nil
			}
			return existingStatus, nil
		}
		return unreceivedStatus(op.Expires), nil
	}

	clear, err := json.Marshal(op)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(clear)
	stored := func() (string, error) {
		if string(encodedDigest) != string(digest[:]) {
			return "", &Problem{Code: "conflict"}
		}
		if code, ok := strings.CutPrefix(existingStatus, "error:"); ok {
			return "", &Problem{Code: code}
		}
		return existingStatus, nil
	}
	if existingStatus != "" {
		return stored()
	}
	if kind != "interaction" {
		switch {
		case op.Expires <= time.Now().UnixMilli():
			return "expired", nil
		case op.Expires > time.Now().Add(150*time.Second).UnixMilli():
			return "", &Problem{Code: "invalid_request"}
		}
	}
	insertArgs := append(append([]any{}, args...), digest[:], op.Expires)
	reservation, err := h.Ledger.db.ExecContext(ctx, `INSERT OR IGNORE INTO remote_intents
          (peer,kind,session,target,intent,digest,expires,status) VALUES (?,?,?,?,?,?,?,'unknown')`, insertArgs...)
	if err != nil {
		return "", err
	}
	inserted, err := reservation.RowsAffected()
	if err != nil {
		return "", err
	}
	if inserted == 0 {
		if err := read(); err != nil {
			return "", err
		}
		return stored()
	}

	var status string
	switch kind {
	case "interaction":
		status, err = h.Controls.Respond(ctx, op.Session, op.Target, op.Reply)
	case "auto_approve":
		var enabled bool
		enabled, err = h.Controls.AutoApprove(ctx, op.Session, op.Enabled)
		status = "disabled"
		if enabled {
			status = "enabled"
		}
	case "sessions":
		var session string
		session, err = h.Controls.CreateSession(ctx)
		if session != "" && err == nil {
			status = "created:" + session
		}
	default:
		status, err = h.Controls.Start(ctx, op.Session, op.Key, op.Text, op.Expires)
	}
	saved := status
	var rejected *Problem
	if errors.As(err, &rejected) {
		saved = "error:" + rejected.Code
	} else if err != nil || status == "" {
		return "unknown", nil
	}
	// The request may have timed out after dispatch. Preserve the durable
	// reservation even if saving the observed outcome now fails.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if _, saveErr := h.Ledger.db.ExecContext(saveCtx, `UPDATE remote_intents SET status=?
         WHERE peer=? AND kind=? AND session=? AND target=? AND intent=?`, append([]any{saved}, args...)...); saveErr != nil {
		return "unknown", nil
	}
	return status, err
}

func unreceivedStatus(expires int64) string {
	if expires <= time.Now().UnixMilli() {
		return "expired"
	}
	return "not_received"
}
