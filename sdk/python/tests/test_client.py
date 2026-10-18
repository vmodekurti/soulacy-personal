import json
from unittest.mock import patch, MagicMock
from soulacy.client import SoulacyClient

def response(value):
    result = MagicMock()
    result.__enter__.return_value.read.return_value = json.dumps(value).encode()
    return result

def test_chat_keeps_session_and_completion_metadata():
    payload = {"reply": "partial", "session_id": "conversation", "metadata": {"outcome": "incomplete"}}
    with patch("soulacy.client.request.urlopen", return_value=response(payload)) as send:
        result = SoulacyClient().chat_response("agent", "continue", session_id="conversation")
        assert result == payload
        assert json.loads(send.call_args.args[0].data)["session_id"] == "conversation"
        assert send.call_args.kwargs["timeout"] == 48 * 60 * 60 + 60

def test_unlimited_timeout_is_supported():
    with patch("soulacy.client.request.urlopen", return_value=response({})) as send:
        SoulacyClient(timeout=0).health()
        assert send.call_args.kwargs["timeout"] is None

def test_existing_string_chat_api_and_custom_timeout():
    with patch("soulacy.client.request.urlopen", return_value=response({"reply": "answer"})) as send:
        assert SoulacyClient(timeout=60).chat("a", "hello") == "answer"
        assert send.call_args.kwargs["timeout"] == 60

def test_agent_ids_are_single_path_components():
    with patch("soulacy.client.request.urlopen", return_value=response({})) as send:
        SoulacyClient().get_agent("a/b?x=1")
        assert send.call_args.args[0].full_url.endswith("/agents/a%2Fb%3Fx%3D1")
