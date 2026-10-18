"""
Soulacy gateway REST client.
Provides a thin Python wrapper around the gateway API so SDK users can
interact with the framework programmatically without needing cURL.
"""

from __future__ import annotations

import json
import os
from typing import Any, Optional
from urllib import request, error
from urllib.parse import quote


class SoulacyClient:
    """REST client for the Soulacy gateway.

    Usage::

        from soulacy import SoulacyClient

        cs = SoulacyClient()  # reads gateway URL from SOULACY_GATEWAY env var

        # List agents
        agents = cs.list_agents()

        # Chat with an agent
        reply = cs.chat(agent_id="my-agent", text="Hello!")
        print(reply)

        # Create an agent from a definition dict
        cs.create_agent({
            "id": "new-agent",
            "name": "New Agent",
            "trigger": "channel",
            "system_prompt": "You are helpful.",
            "llm": {"provider": "ollama", "model": "llama3"},
            "memory": {"read_scopes": ["session"], "write_scopes": ["session"]},
            "enabled": True,
        })
    """

    def __init__(
        self,
        gateway_url: Optional[str] = None,
        api_key: Optional[str] = None,
        timeout: Optional[float] = 48 * 60 * 60 + 60,
    ) -> None:
        self.gateway_url = (
            gateway_url
            or os.environ.get("SOULACY_GATEWAY", "http://localhost:18789")
        ).rstrip("/")
        self.api_key = api_key or os.environ.get("SOULACY_API_KEY", "")
        if timeout is not None and timeout < 0:
            raise ValueError("timeout must be nonnegative, or None for unlimited")
        self.timeout = None if timeout == 0 else timeout

    # ── Agents ────────────────────────────────────────────────────────────────

    def list_agents(self) -> list[dict]:
        return self._get("/agents").get("agents", [])

    def get_agent(self, agent_id: str) -> dict:
        return self._get(f"/agents/{quote(agent_id, safe='')}")

    def create_agent(self, definition: dict) -> dict:
        return self._post("/agents", definition)

    def update_agent(self, agent_id: str, definition: dict) -> dict:
        return self._request("PUT", f"/agents/{quote(agent_id, safe='')}", definition)

    def delete_agent(self, agent_id: str) -> None:
        self._request("DELETE", f"/agents/{quote(agent_id, safe='')}")

    def enable_agent(self, agent_id: str) -> dict:
        return self._post(f"/agents/{quote(agent_id, safe='')}/enable")

    def disable_agent(self, agent_id: str) -> dict:
        return self._post(f"/agents/{quote(agent_id, safe='')}/disable")

    def trigger_agent(self, agent_id: str) -> dict:
        return self._post(f"/agents/{quote(agent_id, safe='')}/trigger")

    # ── Chat ─────────────────────────────────────────────────────────────────

    def chat(
        self,
        agent_id: str,
        text: str,
        user_id: str = "sdk-user",
        username: str = "sdk-user",
        session_id: Optional[str] = None,
    ) -> str:
        return self.chat_response(agent_id, text, user_id, username, session_id).get("reply", "")

    def chat_response(
        self, agent_id: str, text: str, user_id: str = "sdk-user",
        username: str = "sdk-user", session_id: Optional[str] = None,
    ) -> dict:
        """Return the answer, session/run IDs, and completion metadata.

        Pass the returned session_id on the next turn to continue a conversation.
        """
        body = {"agent_id": agent_id, "user_id": user_id, "username": username, "text": text}
        if session_id is not None:
            body["session_id"] = session_id
        return self._post("/chat", body)

    # ── Channels ──────────────────────────────────────────────────────────────

    def list_channels(self) -> dict:
        return self._get("/channels").get("channels", {})

    # ── Schedule ──────────────────────────────────────────────────────────────

    def list_schedule(self) -> list[dict]:
        return self._get("/schedule").get("schedule", [])

    # ── Memory ────────────────────────────────────────────────────────────────

    def list_memory(self, agent_id: str) -> dict:
        return self._get(f"/memory/{quote(agent_id, safe='')}")

    # ── Health ────────────────────────────────────────────────────────────────

    def health(self) -> dict:
        return self._get("/health")

    # ── Internal ──────────────────────────────────────────────────────────────

    def _get(self, path: str) -> dict:
        return self._request("GET", path)

    def _post(self, path: str, body: Any = None) -> dict:
        return self._request("POST", path, body)

    def _request(self, method: str, path: str, body: Any = None) -> dict:
        url = self.gateway_url + "/api/v1" + path
        data = json.dumps(body).encode() if body is not None else None

        req = request.Request(url, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if self.api_key:
            req.add_header("Authorization", f"Bearer {self.api_key}")

        try:
            with request.urlopen(req, timeout=self.timeout) as resp:
                content = resp.read().decode()
                if content:
                    return json.loads(content)
                return {}
        except error.HTTPError as e:
            body_text = e.read().decode()
            raise SoulacyError(f"HTTP {e.code}: {body_text}") from e
        except error.URLError as e:
            raise SoulacyError(
                f"Cannot reach Soulacy gateway at {self.gateway_url}.\n"
                f"Is it running? Try: sy server start\n"
                f"Reason: {e.reason}"
            ) from e


class SoulacyError(Exception):
    """Raised when the gateway returns an error or is unreachable."""
