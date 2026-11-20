package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"

	"github.com/gorilla/websocket"
)

const subprotocol = "agent-done.relay.v1"

var deviceID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var stateError = errors.New("relay connection closed")

type Config struct {
	URL          string            `json:"relay_url"`
	Device       string            `json:"device_id"`
	Token        string            `json:"token"`
	PrivateKey   []byte            `json:"private_key"`
	Peers        map[string][]byte `json:"peers"`
	ControlPeers []string          `json:"control_peers,omitempty"`
}

// ReadConfig refuses configuration that would share its device secrets with
// other local users. Workspace authorization is checked by the Codex adapter.
func ReadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return Config{}, err
	}
	if !stat.Mode().IsRegular() || stat.Mode().Perm()&0077 != 0 || stat.Size() > 64<<10 {
		return Config{}, errors.New("remote config must be a private regular file (0600) of at most 64 KiB")
	}
	var config Config
	decoder := json.NewDecoder(io.LimitReader(f, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil || decoder.Decode(new(any)) != io.EOF {
		return Config{}, errors.New("invalid remote config JSON")
	}
	return config, nil
}

type frame struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	To      string `json:"to,omitempty"`
	From    string `json:"from,omitempty"`
	Device  string `json:"device,omitempty"`
	Status  string `json:"status,omitempty"`
	Payload []byte `json:"payload,omitempty"`
}

type endpoint struct {
	config Config
	cipher *cipher
}

func newEndpoint(config Config) (*endpoint, error) {
	u, err := url.Parse(config.URL)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.Path != "/v1/connect" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || !deviceID.MatchString(config.Device) ||
		len(config.Token) != 43 || len(config.Peers) == 0 {
		return nil, errors.New("remote config needs a WSS relay, device ID, token and a paired peer")
	}
	for peer := range config.Peers {
		if peer == config.Device || !deviceID.MatchString(peer) {
			return nil, errors.New("invalid paired device ID")
		}
	}
	for _, peer := range config.ControlPeers {
		if _, ok := config.Peers[peer]; !ok {
			return nil, errors.New("control peer must have a pinned public key")
		}
	}
	crypt, err := newCipher(config.Device, config.PrivateKey, config.Peers)
	if err != nil {
		return nil, err
	}
	return &endpoint{config, crypt}, nil
}

func (e *endpoint) connect(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second, Subprotocols: []string{subprotocol}}
	ws, response, err := dialer.DialContext(ctx, e.config.URL, http.Header{
		"Authorization": {"Bearer " + e.config.Token},
	})
	if err != nil {
		if response != nil {
			response.Body.Close()
			return nil, fmt.Errorf("relay rejected connection (HTTP %d)", response.StatusCode)
		}
		return nil, errors.New("could not connect to relay")
	}
	if ws.Subprotocol() != subprotocol {
		ws.Close()
		return nil, errors.New("relay selected an unexpected protocol")
	}
	ws.SetReadLimit((maxPayload+2)/3*4 + 1024)
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	ws.SetReadDeadline(time.Now().Add(8 * time.Second))
	var ready frame
	if ws.ReadJSON(&ready) != nil || ready.Type != "ready" || ready.Device != e.config.Device {
		ws.Close()
		return nil, errors.New("relay returned an unexpected device identity")
	}
	ws.SetReadDeadline(time.Now().Add(75 * time.Second))
	handlePing := ws.PingHandler()
	ws.SetPingHandler(func(appData string) error {
		ws.SetReadDeadline(time.Now().Add(75 * time.Second))
		return handlePing(appData)
	})
	return ws, nil
}

func write(ws *websocket.Conn, data frame) error {
	ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return ws.WriteJSON(data)
}

func (e *endpoint) serve(ctx context.Context, handle func(context.Context, string, json.RawMessage) []byte, updates <-chan struct{}, logger *log.Logger) error {
	ws, err := e.connect(ctx)
	if err != nil {
		return err
	}
	defer ws.Close()
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	logger.Print("workstation connected to relay")
	readCtx, finishRead := context.WithCancel(ctx)
	defer finishRead()
	frames := make(chan frame, 1)
	go func() {
		defer close(frames)
		for {
			var incoming frame
			if ws.ReadJSON(&incoming) != nil {
				return
			}
			select {
			case frames <- incoming:
			case <-readCtx.Done():
				return
			}
		}
	}()
	for {
		var incoming frame
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-updates:
			clear := []byte(`{"v":1,"type":"event","event":"state.changed"}`)
			for peer := range e.config.Peers {
				packet, err := e.cipher.seal(peer, clear)
				if err != nil {
					continue
				}
				if write(ws, frame{Type: "send", ID: NewID(), To: peer, Payload: packet}) != nil {
					return stateError
				}
			}
			continue
		case received, ok := <-frames:
			if !ok {
				return stateError
			}
			incoming = received
		}
		switch incoming.Type {
		case "delivery":
			clear, err := e.cipher.open(incoming.From, incoming.Payload)
			if err != nil {
				logger.Print("discarded unauthenticated relay delivery")
				continue
			}
			requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			response := handle(requestCtx, incoming.From, clear)
			cancel()
			if len(response) == 0 {
				continue
			}
			packet, err := e.cipher.seal(incoming.From, response)
			if err != nil {
				logger.Print("could not seal the relay response")
				continue
			}
			if err := write(ws, frame{Type: "send", ID: NewID(), To: incoming.From, Payload: packet}); err != nil {
				return stateError
			}
		case "result":
			if incoming.Status != "forwarded" && incoming.Status != "offline" {
				logger.Print("relay could not forward a response")
			}
		}
	}
}

// Serve connects a workstation and retries a disconnected relay. Its handler
// receives only messages authenticated by a locally pinned peer key.
func Serve(ctx context.Context, config Config, handle func(context.Context, string, json.RawMessage) []byte, updates <-chan struct{}, logger *log.Logger) error {
	e, err := newEndpoint(config)
	if err != nil {
		return err
	}
	delay := time.Second
	for ctx.Err() == nil {
		err := e.serve(ctx, handle, updates, logger)
		if ctx.Err() != nil {
			return nil
		}
		logger.Printf("remote endpoint: %v", err)
		if errors.Is(err, stateError) {
			delay = time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil
		}
		delay = min(delay*2, 30*time.Second)
	}
	return nil
}

// Request sends one command from a paired client and waits for the
// authenticated application response with that command ID.
func Request(ctx context.Context, config Config, to string, data json.RawMessage) (json.RawMessage, error) {
	e, err := newEndpoint(config)
	if err != nil {
		return nil, err
	}
	var expected message
	if err := json.Unmarshal(data, &expected); err != nil || expected.Type != "request" ||
		expected.Version != 1 || !uuid.MatchString(expected.ID) {
		return nil, errors.New("request must be a v1 request with a UUID")
	}
	packet, err := e.cipher.seal(to, data)
	if err != nil {
		return nil, err
	}
	ws, err := e.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer ws.Close()
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	id := NewID()
	if err := write(ws, frame{Type: "send", ID: id, To: to, Payload: packet}); err != nil {
		return nil, stateError
	}
	for {
		var incoming frame
		if err := ws.ReadJSON(&incoming); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, stateError
		}
		switch {
		case incoming.Type == "result" && (incoming.ID == id || incoming.ID == "") && incoming.Status != "forwarded":
			return nil, fmt.Errorf("relay delivery failed: %s", deliveryStatus(incoming.Status))
		case incoming.Type == "delivery" && incoming.From == to:
			clear, err := e.cipher.open(to, incoming.Payload)
			var reply message
			if err == nil && json.Unmarshal(clear, &reply) == nil &&
				reply.Version == 1 && reply.Type == "response" && reply.ID == expected.ID &&
				(len(reply.Result) > 0) != (reply.Error != nil) {
				return clear, nil
			}
		}
	}
}

func deliveryStatus(status string) string {
	switch status {
	case "offline", "forbidden", "busy", "delivery_unknown", "invalid_request", "too_large", "rate_limited":
		return status
	default:
		return "unrecognized status"
	}
}
