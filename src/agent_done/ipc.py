"""Bounded local requests; hooks fail open if attention tracking is unavailable."""

import asyncio
import json
import socket

from .paths import control_socket


def request(message, timeout=1):
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as client:
        client.settimeout(timeout)
        client.connect(str(control_socket()))
        client.sendall(json.dumps(message).encode() + b"\n")
        with client.makefile("rb") as stream:
            result = json.loads(stream.readline(4 * 1024 * 1024))
        if not result.get("ok"):
            raise RuntimeError(result.get("error", "agent-done request failed"))
        return result


async def async_request(message, timeout=1):
    async with asyncio.timeout(timeout):
        reader, writer = await asyncio.open_unix_connection(str(control_socket()), limit=4 * 1024 * 1024)
        try:
            writer.write(json.dumps(message).encode() + b"\n")
            await writer.drain()
            result = json.loads(await reader.readline())
            if not result.get("ok"):
                raise RuntimeError(result.get("error", "agent-done request failed"))
            return result
        finally:
            writer.close()
            await writer.wait_closed()


async def snapshots():
    reader, writer = await asyncio.open_unix_connection(str(control_socket()), limit=4 * 1024 * 1024)
    try:
        writer.write(b'{"op":"subscribe"}\n')
        await writer.drain()
        while line := await reader.readline():
            result = json.loads(line)
            if result.get("ok"):
                yield result["snapshot"]
    finally:
        writer.close()
        await writer.wait_closed()
