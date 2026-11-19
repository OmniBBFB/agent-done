package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func fixtureDevice(id, role, token string, allow ...string) DeviceConfig {
	hash := sha256.Sum256([]byte(token))
	return DeviceConfig{ID: id, Role: role, TokenSHA256: hex.EncodeToString(hash[:]), Allow: allow}
}

const (
	phoneToken  = "0123456789012345678901234567890123456789012"
	firstToken  = "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopq"
	secondToken = "ABCDEFGHIJKLMNOPQRSTUVWXYZABCDEFGHIJKLMNOPQ"
)

func testRelay(t *testing.T, configs ...DeviceConfig) (*relay, *httptest.Server) {
	t.Helper()
	devices, err := newRegistry(Config{Devices: configs})
	if err != nil {
		t.Fatal(err)
	}
	r := newRelay(devices, log.New(io.Discard, "", 0))
	server := httptest.NewTLSServer(r.handler())
	t.Cleanup(server.Close)
	t.Cleanup(r.close)
	return r, server
}

func dial(server *httptest.Server, token, protocol string) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{
		TLSClientConfig: server.Client().Transport.(*http.Transport).TLSClientConfig,
		Subprotocols:    []string{protocol}, HandshakeTimeout: time.Second,
	}
	headers := http.Header{"Authorization": {"Bearer " + token}}
	return dialer.Dial("wss"+strings.TrimPrefix(server.URL, "https")+"/v1/connect", headers)
}

func receive(t *testing.T, conn *websocket.Conn) outgoing {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var message outgoing
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	return message
}

func connect(t *testing.T, server *httptest.Server, token, id string) *websocket.Conn {
	t.Helper()
	conn, response, err := dial(server, token, subprotocol)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if message := receive(t, conn); message.Type != "ready" || message.Device != id || conn.Subprotocol() != subprotocol {
		t.Fatalf("unexpected handshake: %+v", message)
	}
	return conn
}

func send(t *testing.T, conn *websocket.Conn, message incoming) {
	t.Helper()
	if err := conn.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func result(t *testing.T, conn *websocket.Conn, id, status string) outgoing {
	t.Helper()
	message := receive(t, conn)
	if message.Type != "result" || message.ID != id || message.Status != status {
		t.Fatalf("expected result %s/%s, got %+v", id, status, message)
	}
	return message
}

func assertClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, _, err := conn.ReadMessage()
	var netErr net.Error
	if err == nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		t.Fatalf("expected immediate disconnect, read returned %v", err)
	}
}

func TestRelayRoutesOpaqueFramesAcrossTwoWorkstations(t *testing.T) {
	_, server := testRelay(t,
		fixtureDevice("phone", "phone", phoneToken, "alpha", "beta", "restricted"),
		fixtureDevice("alpha", "workstation", firstToken, "phone"),
		fixtureDevice("beta", "workstation", secondToken, "phone"),
		fixtureDevice("restricted", "workstation", strings.Repeat("r", 43)),
	)
	phone := connect(t, server, phoneToken, "phone")
	alpha := connect(t, server, firstToken, "alpha")

	send(t, phone, incoming{Type: "presence", ID: "presence", To: "beta"})
	if message := result(t, phone, "presence", "ok"); message.Online == nil || *message.Online {
		t.Fatalf("expected an offline workstation: %+v", message)
	}
	send(t, phone, incoming{Type: "send", ID: "offline", To: "beta", Payload: []byte{1}})
	result(t, phone, "offline", "offline")

	beta := connect(t, server, secondToken, "beta")
	send(t, phone, incoming{Type: "presence", ID: "online", To: "beta"})
	if message := result(t, phone, "online", "ok"); message.Online == nil || !*message.Online {
		t.Fatalf("expected an online workstation: %+v", message)
	}

	payload := []byte{0, 255, 13, 41, 10, 0}
	send(t, phone, incoming{Type: "send", ID: "second", To: "beta", Payload: payload})
	result(t, phone, "second", "forwarded")
	message := receive(t, beta)
	if message.Type != "delivery" || message.From != "phone" || !bytes.Equal(message.Payload, payload) {
		t.Fatalf("invalid relay delivery or unexpected offline replay: %+v", message)
	}
	send(t, phone, incoming{Type: "send", ID: "first", To: "alpha", Payload: []byte{37}})
	result(t, phone, "first", "forwarded")
	if message := receive(t, alpha); message.Type != "delivery" || !bytes.Equal(message.Payload, []byte{37}) {
		t.Fatalf("other workstation received the wrong payload: %+v", message)
	}

	send(t, beta, incoming{Type: "send", ID: "reply", To: "phone", Payload: payload})
	result(t, beta, "reply", "forwarded")
	if message := receive(t, phone); message.From != "beta" || !bytes.Equal(message.Payload, payload) {
		t.Fatalf("reply did not identify the authenticated sender: %+v", message)
	}
	send(t, phone, incoming{Type: "send", ID: "forbid", To: "restricted", Payload: []byte{1}})
	result(t, phone, "forbid", "forbidden")
	send(t, phone, incoming{Type: "send", ID: "limit", To: "beta", Payload: make([]byte, maxPayload+1)})
	result(t, phone, "limit", "too_large")
}

func TestRelayRejectsCredentialsAndProtocolMismatch(t *testing.T) {
	_, server := testRelay(t, fixtureDevice("phone", "phone", phoneToken))
	for _, test := range []struct {
		token, version string
		status         int
	}{
		{"bogus", subprotocol, http.StatusUnauthorized},
		{phoneToken, "agent-done.relay.v2", http.StatusBadRequest},
	} {
		conn, response, err := dial(server, test.token, test.version)
		if conn != nil {
			conn.Close()
		}
		if response != nil {
			response.Body.Close()
		}
		if err == nil || response == nil || response.StatusCode != test.status {
			t.Fatalf("bad connection not rejected as expected: %v %v", response, err)
		}
	}
}

func TestRelayReconnectAndCredentialRevocation(t *testing.T) {
	phoneDevice := fixtureDevice("phone", "phone", phoneToken, "alpha")
	r, server := testRelay(t, phoneDevice, fixtureDevice("alpha", "workstation", firstToken, "phone"))
	phone := connect(t, server, phoneToken, "phone")
	old := connect(t, server, firstToken, "alpha")
	current := connect(t, server, firstToken, "alpha")
	assertClosed(t, old)

	next, err := newRegistry(Config{Devices: []DeviceConfig{phoneDevice, fixtureDevice("alpha", "workstation", secondToken, "phone")}})
	if err != nil {
		t.Fatal(err)
	}
	r.reload(next)
	assertClosed(t, current)
	rejected, response, err := dial(server, firstToken, subprotocol)
	if rejected != nil {
		rejected.Close()
	}
	if response != nil {
		response.Body.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the revoked token could reconnect: %v %v", response, err)
	}
	send(t, phone, incoming{Type: "send", ID: "revoked", To: "alpha", Payload: []byte{1}})
	result(t, phone, "revoked", "offline")

	current = connect(t, server, secondToken, "alpha")
	send(t, phone, incoming{Type: "send", ID: "reconnected", To: "alpha", Payload: []byte{2}})
	result(t, phone, "reconnected", "forwarded")
	if message := receive(t, current); !bytes.Equal(message.Payload, []byte{2}) {
		t.Fatalf("incorrect delivery after credential rotation: %+v", message)
	}
}
