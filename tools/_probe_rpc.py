"""Minimal synchronous local WebSocket JSON-RPC client for probe cleanup."""

import base64
import json
import os
import socket
import struct
import time


class ProbeRPC:
    def __init__(self):
        self.socket = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.socket.settimeout(10)
        self.socket.connect("/home/csc/.codex/app-server-control/app-server-control.sock")
        self.stream = self.socket.makefile("rb")
        key = base64.b64encode(os.urandom(16)).decode()
        self.socket.sendall(("GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n"
                             "Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"
                             "Sec-WebSocket-Key: " + key + "\r\n\r\n").encode())
        status = self.stream.readline()
        if b"101" not in status:
            raise RuntimeError("daemon WebSocket handshake failed")
        while self.stream.readline() != b"\r\n":
            pass
        self.sequence = 0
        self.events = []
        self.request("initialize", {"clientInfo": {"name": "agent_done_probe", "version": "0.1"},
                                    "capabilities": {"experimentalApi": True}})
        self.send({"method": "initialized"})

    def send(self, obj):
        payload = json.dumps(obj).encode()
        size = len(payload)
        prefix = b"\x81"
        if size < 126:
            prefix += bytes([128 | size])
        elif size < 65536:
            prefix += b"\xfe" + struct.pack("!H", size)
        else:
            prefix += b"\xff" + struct.pack("!Q", size)
        mask = os.urandom(4)
        self.socket.sendall(prefix + mask + bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload)))

    def receive(self):
        header = self.stream.read(2)
        if len(header) != 2:
            raise EOFError("daemon disconnected")
        first, second = header
        size = second & 127
        if size == 126:
            size = struct.unpack("!H", self.stream.read(2))[0]
        elif size == 127:
            size = struct.unpack("!Q", self.stream.read(8))[0]
        mask = self.stream.read(4) if second & 128 else b""
        payload = self.stream.read(size)
        if mask:
            payload = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
        if first & 15 != 1:
            raise RuntimeError("unexpected daemon frame")
        return json.loads(payload)

    def request(self, method, params=None):
        self.sequence += 1
        ident = self.sequence
        self.send({"id": ident, "method": method, "params": params or {}})
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            response = self.receive()
            if response.get("id") == ident and "method" not in response:
                if "error" in response:
                    raise RuntimeError(json.dumps(response["error"]))
                return response.get("result")
            self.events.append(response)
        raise TimeoutError(method)

    def close(self):
        self.stream.close()
        self.socket.close()
