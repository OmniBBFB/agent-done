package remote

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
)

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

func NewID() string {
	var raw [16]byte
	rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

type message struct {
	Version int             `json:"v"`
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Problem        `json:"error,omitempty"`
}

type Problem struct {
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

func (p *Problem) Error() string { return p.Code }

type query struct {
	Cursor string `json:"cursor"`
	Limit  *int   `json:"limit"`
	SID    string `json:"session_id"`
}

func (q query) limit() int {
	if q.Limit == nil {
		return 20
	}
	return *q.Limit
}

type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

type Session struct {
	ID             string `json:"session_id"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Updated        int64  `json:"updated_at_ms"`
	CanSendMessage bool   `json:"can_send_message,omitempty"`
}

type HistoryItem struct {
	ID        string `json:"message_id"`
	Role      string `json:"role"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

type Catalog interface {
	Sessions(context.Context, string, int) (Page[Session], error)
	History(context.Context, string, string, int) (Page[HistoryItem], error)
}

type Handler struct {
	Catalog  Catalog
	Controls Controls
	Ledger   *Ledger
}

func (h Handler) Handle(ctx context.Context, peer string, clear json.RawMessage) []byte {
	var request message
	if json.Unmarshal(clear, &request) != nil || request.Type != "request" ||
		request.Version != 1 || !uuid.MatchString(request.ID) {
		return nil
	}
	out := message{Version: 1, Type: "response", ID: request.ID}
	data, problem := h.dispatch(ctx, peer, request.Method, request.Params)
	out.Error = problem
	if problem == nil {
		raw, err := json.Marshal(data)
		if err != nil || len(raw) > maxPayload-keyLength-tagLength-256 {
			out.Error = &Problem{Code: "unavailable"}
		} else {
			out.Result = raw
		}
	}
	raw, _ := json.Marshal(out)
	return raw
}

func (h Handler) dispatch(ctx context.Context, peer, method string, params json.RawMessage) (any, *Problem) {
	if len(params) == 0 || params[0] != '{' {
		return nil, &Problem{Code: "invalid_request"}
	}
	switch method {
	case "sessions.list", "history.page", "interactions.snapshot":
		var q query
		if json.Unmarshal(params, &q) != nil || q.limit() < 1 || q.limit() > 50 ||
			len(q.Cursor) > 4096 || len(q.SID) > 256 {
			return nil, &Problem{Code: "invalid_request"}
		}
		if method == "sessions.list" {
			page, err := h.Catalog.Sessions(ctx, q.Cursor, q.limit())
			return page, catalogProblem(err)
		}
		if method == "interactions.snapshot" {
			if h.Controls == nil {
				return Page[Interaction]{Items: []Interaction{}}, nil
			}
			data, err := h.Controls.Interactions(ctx, q.SID, q.Cursor, q.limit())
			return data, catalogProblem(err)
		}
		if q.SID == "" {
			return nil, &Problem{Code: "invalid_request"}
		}
		page, err := h.Catalog.History(ctx, q.SID, q.Cursor, q.limit())
		return page, catalogProblem(err)

	case "interaction.respond", "interaction.status", "messages.send", "messages.status":
		if h.Controls == nil || h.Ledger == nil {
			return nil, &Problem{Code: "unavailable", Reason: "control_not_enabled"}
		}
		return h.control(ctx, peer, method, params)
	}
	return nil, &Problem{Code: "invalid_request"}
}

func catalogProblem(err error) *Problem {
	if err == nil {
		return nil
	}
	if problem, ok := err.(*Problem); ok {
		return problem
	}
	return &Problem{Code: "unavailable"}
}
