# Relay transport v1

This document defines the outer transport. The encrypted payload's format is
described separately in the [control protocol v1 candidate](control-v1.md).

## Connection

- Connect using WSS to `GET /v1/connect` with WebSocket subprotocol
  `agent-done.relay.v1` and the HTTP header `Authorization: Bearer <token>`.
  Native clients omit the browser Origin header.
- The relay authenticates the credential before upgrading, then sends
  `{"type":"ready","device":"phone-1"}`. Expect this before sending requests.
  A valid new connection replaces the prior connection for the same device.
- The relay sends WebSocket Ping frames every 20 seconds. Clients answer with
  Pong frames. A quiet socket without Pong expires after 60 seconds.
- The relay uses the authenticated device identity. It permits routes between a
  phone and a workstation when each device lists the other in its allowlist.

## Frames

Each text frame contains one JSON object. Request IDs and device IDs have 1–64
ASCII characters from letters, digits, underscore and hyphen. The first
character is a letter or digit. IDs correlate outer requests and results.
Clients use distinct request IDs among their in-flight requests.

### Send

```json
{"type":"send","id":"send-1","to":"workstation-2","payload":"AP8="}
```

`payload` is an opaque byte sequence encoded as standard padded Base64. The
maximum decoded length is 256 KiB. Clients seal content before sending it.
The relay sends the destination a delivery and returns a separate result.
Order between the two receiving clients is unspecified.

```json
{"type":"delivery","from":"phone-1","payload":"AP8="}
```

```json
{"type":"result","id":"send-1","status":"forwarded"}
```

`forwarded` means the relay wrote the frame to the target WebSocket. The
destination's encrypted response carries the application outcome. A relay
result never grants an approval or confirms that Codex acted.

### Presence

```json
{"type":"presence","id":"presence-1","to":"workstation-2"}
```

```json
{"type":"result","id":"presence-1","status":"ok","online":true}
```

Online describes a live relay connection. Clients can refresh presence after
reconnect. No OS, App build or Codex health fields are collected.

| Result | Meaning |
| --- | --- |
| `ok` | A presence query completed |
| `forwarded` | The frame was written to the destination socket |
| `offline` | No matching live destination connection was available before the write |
| `forbidden` | Route unavailable for this device, including unknown targets |
| `busy` | The destination writer was busy and this frame was not queued |
| `delivery_unknown` | Write failed after starting. The destination might have received the frame |
| `invalid_request` | Invalid frame or request structure |
| `too_large` | Decoded payload exceeded 256 KiB |
| `rate_limited` | More than 30 frames in a one-second window. The server closes the connection |

The relay might omit the result ID for invalid input or connection-level rate
limiting. An oversized WebSocket frame can cause an immediate close. On
disconnect the relay discards all connection state and never queues offline
messages. Before retrying a control action with an uncertain result, synchronize
its encrypted application state with the workstation.

## Running the Go relay

```sh
go build -o dist/agent-done-relay ./cmd/agent-done-relay
./dist/agent-done-relay credential
./dist/agent-done-relay serve -listen 0.0.0.0:8443 -config relay.json \
  -tls-cert /path/to/fullchain.pem -tls-key /path/to/key.pem
```

Run `credential` once per device and give its token to that device through a
trusted channel. Save only its `token_sha256` in the registry. For two devices,
the registry has this shape. The digest values below are placeholders.

```json
{
  "devices": [
    {"id":"phone-1","role":"phone","token_sha256":"<digest-1>","allow":["workstation-2"]},
    {"id":"workstation-2","role":"workstation","token_sha256":"<digest-2>","allow":["phone-1"]}
  ]
}
```

Update the registry file atomically, then send SIGHUP to the relay process.
Removing a device or replacing its digest disconnects sessions using the old
credential. A malformed reload leaves the previous registry active. A local
test can use `-dev-insecure` with an explicit loopback IP and no TLS flags.
`GET /healthz` returns HTTP 204 while the HTTP server is reachable.
