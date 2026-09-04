"""
Agent decorator — define a Soulacy agent in Python.

The @agent decorator registers a Python function as an agent definition
and provides a .serve() method that starts a lightweight HTTP server.
The Soulacy gateway calls this server when the agent is triggered.

Usage::

    from soulacy import agent, tool

    @tool
    def get_time() -> str:
        \"\"\"Return the current UTC time.\"\"\"
        from datetime import datetime, timezone
        return datetime.now(timezone.utc).isoformat()

    @agent(
        id="time-bot",
        name="Time Bot",
        system_prompt="You are a time assistant. Use get_time to answer questions.",
        tools=[get_time],
        channels=["telegram", "http"],
    )
    def time_bot():
        pass  # Logic is handled by the gateway's LLM loop

    if __name__ == "__main__":
        time_bot.serve()
"""

from __future__ import annotations

import json
import os
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any, Callable, List, Optional

from .client import SoulacyClient
from .tool import Tool


class AgentRunner:
    """Wraps a Python agent definition and provides a .serve() method."""

    def __init__(
        self,
        id: str,
        name: str,
        system_prompt: str,
        tools: List[Tool],
        channels: List[str],
        llm_provider: str,
        llm_model: str,
        trigger: str,
        cron: Optional[str],
        enabled: bool,
        fn: Optional[Callable],
    ) -> None:
        self.id = id
        self.name = name
        self.system_prompt = system_prompt
        self.tools = tools
        self.channels = channels
        self.llm_provider = llm_provider
        self.llm_model = llm_model
        self.trigger = trigger
        self.cron = cron
        self.enabled = enabled
        self.fn = fn

    def definition(self) -> dict:
        """Return the agent definition dict suitable for the gateway API."""
        d: dict[str, Any] = {
            "id": self.id,
            "name": self.name,
            "system_prompt": self.system_prompt,
            "trigger": self.trigger,
            "channels": self.channels,
            "llm": {
                "provider": self.llm_provider,
                "model": self.llm_model,
                "temperature": 0.7,
                "max_tokens": 2048,
            },
            "memory": {
                "read_scopes": ["session"],
                "write_scopes": ["session"],
                "max_tokens": 30,
            },
            "tools": [t.schema() for t in self.tools],
            "enabled": self.enabled,
            "stream_reply": False,
            "max_turns": 10,
        }
        if self.cron:
            d["schedule"] = {"cron": self.cron}
        return d

    def register(self, gateway_url: Optional[str] = None, api_key: Optional[str] = None) -> None:
        """Push this agent definition to the Soulacy gateway."""
        client = SoulacyClient(gateway_url=gateway_url, api_key=api_key)
        try:
            client.create_agent(self.definition())
            print(f"✓ Agent '{self.id}' registered with Soulacy.")
        except Exception as e:
            print(f"✗ Failed to register agent '{self.id}': {e}")

    def serve(
        self,
        host: str = "127.0.0.1",
        port: Optional[int] = None,
        auto_register: bool = True,
        gateway_url: Optional[str] = None,
        api_key: Optional[str] = None,
    ) -> None:
        """Start a lightweight HTTP server that the gateway can call for tool execution.

        If auto_register=True, the agent definition is pushed to the gateway on startup.
        """
        if port is None:
            port = int(os.environ.get("SOULACY_AGENT_PORT", "19000"))

        if auto_register:
            self.register(gateway_url=gateway_url, api_key=api_key)

        tools_by_name = {t.name: t for t in self.tools}

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                body = self.rfile.read(length)
                req = json.loads(body) if body else {}

                tool_name = req.get("tool")
                kwargs = req.get("arguments", {})

                if tool_name not in tools_by_name:
                    self._respond(404, {"error": f"tool '{tool_name}' not found"})
                    return

                try:
                    result = tools_by_name[tool_name](**kwargs)
                    self._respond(200, {"result": result})
                except Exception as exc:
                    self._respond(500, {"error": str(exc)})

            def _respond(self, status: int, data: dict) -> None:
                body = json.dumps(data).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, fmt, *args):
                pass  # suppress default request logging

        server = HTTPServer((host, port), Handler)
        print(f"Soulacy agent '{self.id}' tool server running on {host}:{port}")
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            print(f"\nAgent '{self.id}' stopped.")


def agent(
    id: str,
    name: Optional[str] = None,
    system_prompt: str = "You are a helpful assistant.",
    tools: Optional[List[Tool]] = None,
    channels: Optional[List[str]] = None,
    llm_provider: str = "ollama",
    llm_model: str = "llama3",
    trigger: str = "channel",
    cron: Optional[str] = None,
    enabled: bool = True,
) -> Callable:
    """Decorator that turns a Python function into a Soulacy AgentRunner.

    Args:
        id:            Unique agent identifier (slug).
        name:          Human-readable display name.
        system_prompt: The agent's system prompt.
        tools:         List of Tool instances the agent can call.
        channels:      List of channel IDs this agent listens on.
        llm_provider:  LLM provider ID (default: "ollama").
        llm_model:     Model name (default: "llama3").
        trigger:       "channel", "cron", "oneshot", or "webhook".
        cron:          Cron expression (required when trigger="cron").
        enabled:       Whether the agent is active on registration.

    Returns:
        AgentRunner instance that wraps the decorated function.
    """
    def decorator(fn: Callable) -> AgentRunner:
        return AgentRunner(
            id=id,
            name=name or id.replace("-", " ").title(),
            system_prompt=system_prompt,
            tools=tools or [],
            channels=channels or ["http"],
            llm_provider=llm_provider,
            llm_model=llm_model,
            trigger=trigger,
            cron=cron,
            enabled=enabled,
            fn=fn,
        )
    return decorator
