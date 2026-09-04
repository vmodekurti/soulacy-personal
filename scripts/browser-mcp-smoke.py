#!/usr/bin/env python3
"""Optional Playwright MCP sidecar smoke test for release candidates.

Starts the configured browser MCP command over stdio, performs the MCP
initialize/tools-list handshake, verifies browser tools are advertised, and
then terminates the sidecar. It intentionally does not navigate anywhere: the
goal is to catch broken installs/configuration without opening pages or
leaving browser processes behind.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
from typing import Any


def split_args(raw: str) -> list[str]:
    # The default command is simple and the env override is for CI operators.
    # Avoid shell=True so arbitrary strings are not executed through a shell.
    import shlex

    return shlex.split(raw)


def read_json_line(proc: subprocess.Popen[str], timeout: float) -> dict[str, Any]:
    deadline = time.time() + timeout
    while time.time() < deadline:
        line = proc.stdout.readline() if proc.stdout else ""
        if not line:
            if proc.poll() is not None:
                err = proc.stderr.read() if proc.stderr else ""
                raise RuntimeError(f"sidecar exited early with status {proc.returncode}: {err[-2000:]}")
            time.sleep(0.05)
            continue
        line = line.strip()
        if not line:
            continue
        try:
            return json.loads(line)
        except json.JSONDecodeError:
            # Some tools accidentally print startup logs. Ignore non-JSON lines
            # until the timeout; protocol violations still fail if no JSON comes.
            continue
    raise TimeoutError("timed out waiting for MCP JSON response")


def send(proc: subprocess.Popen[str], payload: dict[str, Any]) -> None:
    if not proc.stdin:
        raise RuntimeError("sidecar stdin is closed")
    proc.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
    proc.stdin.flush()


def request(proc: subprocess.Popen[str], payload: dict[str, Any], timeout: float) -> dict[str, Any]:
    send(proc, payload)
    while True:
        msg = read_json_line(proc, timeout)
        if msg.get("id") == payload.get("id"):
            if "error" in msg:
                raise RuntimeError(f"MCP request {payload.get('method')} failed: {msg['error']}")
            return msg


def main() -> int:
    if os.environ.get("SOULACY_BROWSER_MCP_SMOKE", "0") != "1":
        print("browser MCP smoke skipped; set SOULACY_BROWSER_MCP_SMOKE=1 to run")
        return 0

    cmd = split_args(
        os.environ.get(
            "SOULACY_BROWSER_MCP_COMMAND",
            "npx -y @playwright/mcp@latest --browser chromium --headless",
        )
    )
    if not cmd:
        raise SystemExit("SOULACY_BROWSER_MCP_COMMAND is empty")
    if shutil.which(cmd[0]) is None:
        print(f"browser MCP smoke skipped; command not found: {cmd[0]}")
        return 0

    timeout = float(os.environ.get("SOULACY_BROWSER_MCP_TIMEOUT", "45"))
    proc = subprocess.Popen(
        cmd,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )
    try:
        init = request(
            proc,
            {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2024-11-05",
                    "capabilities": {},
                    "clientInfo": {"name": "soulacy-browser-smoke", "version": "1.0"},
                },
            },
            timeout,
        )
        result = init.get("result") or {}
        if "capabilities" not in result:
            raise RuntimeError(f"initialize response missing capabilities: {init}")

        send(proc, {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}})
        tools_msg = request(proc, {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}, timeout)
        tools = (tools_msg.get("result") or {}).get("tools") or []
        names = {str(t.get("name", "")) for t in tools if isinstance(t, dict)}
        required_any = [
            {"browser_navigate", "navigate"},
            {"browser_screenshot", "screenshot"},
        ]
        missing = []
        for group in required_any:
            found = False
            for candidate in group:
                if candidate in names or any(tool_name.endswith("_" + candidate) for tool_name in names):
                    found = True
                    break
            if not found:
                missing.append("/".join(sorted(group)))
        if missing:
            raise RuntimeError(f"browser MCP tools missing {missing}; got {sorted(names)[:30]}")
        print(f"browser MCP smoke passed; advertised {len(names)} tool(s)")
        return 0
    finally:
        try:
            send(proc, {"jsonrpc": "2.0", "id": 99, "method": "shutdown", "params": {}})
        except Exception:
            pass
        try:
            proc.terminate()
            proc.wait(timeout=5)
        except Exception:
            proc.kill()
            proc.wait(timeout=5)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"browser MCP smoke failed: {exc}", file=sys.stderr)
        raise SystemExit(1)
