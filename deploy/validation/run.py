"""Run on an isolated real host: no production credentials, volumes or egress.

The provider is an explicit local fixture. This tests the actual compiled
gateway and HTTP/SQLite/restart boundaries, not model quality or real devices.
"""
from concurrent.futures import ThreadPoolExecutor
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import json
import os
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


class FixtureProvider(BaseHTTPRequestHandler):
    calls = 0
    lock = threading.Lock()
    def log_message(self, *_args):
        pass

    def reply(self, body):
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        self.reply({"object": "list", "data": [{"id": "validation-model", "object": "model"}]})

    def do_POST(self):
        with FixtureProvider.lock:
            FixtureProvider.calls += 1
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
        users = [m.get("content", "") for m in body.get("messages", []) if m.get("role") == "user"]
        latest = str(users[-1]) if users else ""
        output = "Missing required evidence" if "force-failure" in latest else "VERIFIED Sources: local validation fixture"
        systems = [m.get("content", "") for m in body.get("messages", []) if m.get("role") == "system"]
        if any("Extract ONE genuinely reusable lesson" in s for s in systems):
            note = json.loads(latest)["note"]
            output = json.dumps({"lesson": {"key": "reporting-units", "kind": "preference",
                "title": "Reporting units", "trigger": "Writing reports with measurements",
                "content": "Use metric units in reports.", "verification": "Check the current request for exceptions.",
                "citations": [{"source_id": "user", "quote": note}]}})
        self.reply({"id": "fixture-response", "object": "chat.completion", "model": "validation-model",
                    "choices": [{"index": 0, "message": {"role": "assistant", "content": output}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": 10, "completion_tokens": 10, "total_tokens": 20}})


BASE = "http://127.0.0.1:18789"
TOKEN = "isolated-validation-not-a-production-credential"
passed = []


class FixtureChanges(BaseHTTPRequestHandler):
    """Two explicit conditional-write protocol fixtures, not vendor SaaS APIs."""
    lock = threading.Lock()
    record = {"status": "open", "note": "original note"}
    document = "Original document"
    versions = {"/record": 1, "/document": 1}
    writes = {"/record": 0, "/document": 0}
    fail_record_next = False

    def log_message(self, *_args):
        pass

    def do_GET(self):
        with self.lock:
            raw = json.dumps(self.record).encode() if self.path == "/record" else self.document.encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json" if self.path == "/record" else "text/plain; charset=utf-8")
            self.send_header("ETag", f'"v{self.versions[self.path]}"')
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    def mutate(self):
        with self.lock:
            if self.headers.get("If-Match") != f'"v{self.versions[self.path]}"':
                self.send_response(412)
                self.end_headers()
                return
            raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            if self.path == "/record" and self.command == "PATCH":
                result = dict(self.record)
                for op in json.loads(raw):
                    field = op["path"][1:].replace("~1", "/").replace("~0", "~")
                    if op["op"] == "test":
                        if field not in result or result[field] != op["value"]:
                            self.send_response(409)
                            self.end_headers()
                            return
                    elif op["op"] == "remove":
                        del result[field]
                    elif op["op"] in ("add", "replace"):
                        result[field] = op["value"]
                    else:
                        raise AssertionError("Unexpected JSON Patch operation")
                type(self).record = result
            elif self.path == "/document" and self.command == "PUT":
                type(self).document = raw.decode()
            else:
                raise AssertionError("Unexpected fixture write")
            self.writes[self.path] += 1
            self.versions[self.path] += 1
            status = 204
            if self.path == "/record" and self.fail_record_next:
                type(self).fail_record_next = False
                status = 500  # The change happened, but its acknowledgement was lost.
            self.send_response(status)
            self.end_headers()

    do_PATCH = mutate
    do_PUT = mutate


def safe_undo_checks():
    path = "/api/v1/agents/validation-agent/safe-undo/jobs"
    changes = {"title": "Customer and document", "changes": [
        {"resource_id": "customer", "fields": {"status": "done"}},
        {"resource_id": "document", "text": "Updated document"}]}
    draft = request("POST", path, changes, status=201)
    receipt = path + "/" + draft["id"]
    check("Safe Undo prepare performs no external writes", sum(FixtureChanges.writes.values()) == 0 and draft["status"] == "draft")
    summaries = request("GET", path)
    check("Safe Undo lists omit private before/after contents", "before_text" not in json.dumps(summaries))
    review = request("POST", receipt + "/preview", {"direction": "apply"})
    with FixtureChanges.lock:
        FixtureChanges.record["note"] = "A newer teammate note"
        FixtureChanges.versions["/record"] += 1
    request("POST", receipt + "/execute", {"token": review["token"], "confirmed": True}, status=409)
    check("Safe Undo refuses a stale preview before any writes", sum(FixtureChanges.writes.values()) == 0)
    review = request("POST", receipt + "/preview", {"direction": "apply"})
    request("POST", receipt + "/execute", {"token": review["token"], "confirmed": False}, status=400)
    confirm = {"token": review["token"], "confirmed": True}
    applied = request("POST", receipt + "/execute", confirm)
    check("Safe Undo applies two different conditional-write protocols", applied["status"] == "applied" and FixtureChanges.document == "Updated document" and FixtureChanges.record["status"] == "done")
    with ThreadPoolExecutor(max_workers=8) as pool:
        duplicates = list(pool.map(lambda _: request("POST", receipt + "/execute", confirm), range(8)))
    check("Safe Undo concurrent duplicate confirmations write once", all(j["status"] == "applied" for j in duplicates) and sum(FixtureChanges.writes.values()) == 2)
    undo = request("POST", receipt + "/preview", {"direction": "undo"})
    check("Safe Undo reverses systems in reverse order", [s["index"] for s in undo["steps"]] == [1, 0])
    undone = request("POST", receipt + "/execute", {"token": undo["token"], "confirmed": True})
    check("Safe Undo preserves newer unrelated record fields", undone["status"] == "undone" and FixtureChanges.record == {"status": "open", "note": "A newer teammate note"} and FixtureChanges.document == "Original document")
    unknown = request("POST", path, changes, status=201)
    unknown_path = path + "/" + unknown["id"]
    review = request("POST", unknown_path + "/preview", {"direction": "apply"})
    with FixtureChanges.lock:
        FixtureChanges.fail_record_next = True
    result = request("POST", unknown_path + "/execute", {"token": review["token"], "confirmed": True})
    check("Safe Undo quarantines an applied write with a lost acknowledgement", result["status"] == "needs_review" and FixtureChanges.writes["/document"] == 2)
    request("POST", unknown_path + "/preview", {"direction": "undo"}, status=409)
    request("POST", unknown_path + "/preview", {"direction": "apply"}, status=409)
    return unknown_path, review["token"]


def safe_undo_after_restart(path, token):
    before = sum(FixtureChanges.writes.values())
    receipt = request("POST", path + "/execute", {"token": token, "confirmed": True})
    check("Safe Undo restart retains unknown state without replay", receipt["status"] == "needs_review" and sum(FixtureChanges.writes.values()) == before)
    review = request("POST", path + "/preview", {"direction": "reconcile"})
    check("Safe Undo reconciliation explicitly shows observed outcome", review["steps"] == [{"index": 0, "result": "applied"}])
    accepted = request("POST", path + "/execute", {"token": review["token"], "confirmed": True})
    check("Safe Undo reconciliation makes no external writes", accepted["actions"][0]["reconciled"] and sum(FixtureChanges.writes.values()) == before)
    review = request("POST", path + "/preview", {"direction": "apply"})
    result = request("POST", path + "/execute", {"token": review["token"], "confirmed": True})
    check("Safe Undo resumes only the pending external action", result["status"] == "applied" and sum(FixtureChanges.writes.values()) == before + 1)
    review = request("POST", path + "/preview", {"direction": "undo"})
    result = request("POST", path + "/execute", {"token": review["token"], "confirmed": True})
    check("Safe Undo remains reversible after restart and operator reconciliation", result["status"] == "undone" and FixtureChanges.document == "Original document" and FixtureChanges.record["note"] == "A newer teammate note")


def learning_checks():
    path = "/api/v1/agents/validation-agent/learning"
    note = {"note": "Use metric units in reports."}
    request("GET", path + "/lessons", status=401, authenticated=False)
    request("GET", path + "/lessons?api_key=" + TOKEN, status=403, authenticated=False)
    check("Learning refuses unauthenticated and URL-key access", True)
    result = request("POST", path + "/teach", note)
    lesson = result["lesson"]
    lesson_path = path + "/lessons/" + lesson["id"]
    check("Learning teach creates a cited pending draft without activation", result["created"] and lesson["status"] == "pending" and lesson["sources"][0]["text"] == note["note"] and not request("GET", path + "/lessons")["lessons"])
    request("POST", lesson_path + "/review", {"action": "approve", "confirmed": False}, status=400)
    approved = request("POST", lesson_path + "/review", {"action": "approve", "confirmed": True})
    check("Learning requires explicit approval to activate", approved["status"] == "active")
    with ThreadPoolExecutor(max_workers=8) as pool:
        duplicates = list(pool.map(lambda _: request("POST", lesson_path + "/review", {"action": "approve", "confirmed": True}), range(8)))
    check("Learning concurrent approvals are idempotent", all(l["id"] == lesson["id"] for l in duplicates) and request("GET", path + "/lessons")["total"] == 1)
    duplicate = request("POST", path + "/teach", note)
    check("Learning duplicate teaching does not multiply lessons", not duplicate["created"] and duplicate["lesson"]["id"] == lesson["id"])
    for _ in range(2):
        voted = request("POST", lesson_path + "/feedback", {"rating": -1})
    check("Learning feedback counts one current human vote", voted["unhelpful"] == 1 and voted.get("helpful", 0) == 0)
    request("POST", lesson_path + "/review", {"action": "archive", "confirmed": True})
    request("POST", lesson_path + "/review", {"action": "approve", "confirmed": True}, status=409)
    check("Learning disabled guidance cannot silently reactivate", not request("GET", path + "/lessons")["lessons"])
    restored = request("POST", lesson_path + "/review", {"action": "restore", "confirmed": True})
    check("Learning restoration makes a new reviewable revision", restored["status"] == "pending" and restored["id"] != lesson["id"] and restored["version"] > lesson["version"])
    restored_path = path + "/lessons/" + restored["id"]
    request("POST", restored_path + "/review", {"action": "approve", "confirmed": True})
    return restored_path


def check(name, condition):
    if not condition:
        raise AssertionError(name)
    passed.append(name)
    print("PASS", name, flush=True)


def request(method, path, body=None, *, status=200, key=None, authenticated=True):
    headers = {"Content-Type": "application/json"}
    if authenticated:
        headers["Authorization"] = "Bearer " + TOKEN
    if key:
        headers["Idempotency-Key"] = key
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=30) as response:
            actual, raw = response.status, response.read()
    except urllib.error.HTTPError as exc:
        actual, raw = exc.code, exc.read()
    if actual != status:
        raise AssertionError(f"{method} {path}: expected {status}, got {actual}: {raw[:1200]!r}")
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw.decode()


def start_gateway(env, log):
    process = subprocess.Popen(["/validation/soulacy", "serve"], env=env, stdout=log, stderr=log)
    for _ in range(120):
        if process.poll() is not None:
            raise RuntimeError(f"gateway exited at startup: {process.returncode}")
        try:
            request("GET", "/api/v1/health")
            return process
        except (OSError, AssertionError):
            time.sleep(0.25)
    process.terminate()
    process.wait(timeout=15)
    raise TimeoutError("gateway never became healthy")


def stop_gateway(process):
    process.terminate()
    try:
        process.wait(timeout=20)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)
        raise AssertionError("gateway failed graceful shutdown")
    check("gateway exits cleanly", process.returncode == 0)


def main():
    suites = [("autopilot", "."), ("gateway", "TestAutopilot|TestDiscovery|TestListen|TestPublished|TestSafeUndo|TestLearningNotebook|TestModelPreparation|TestGatewayChatStream"), ("runtime", "TestManaged|TestApproved|TestSafeUndo|TestLearning|TestTeachLearning|TestModelPreparation"),
              ("mobile", "TestNode"), ("llm", "TestRunCost|TestModelProfile|TestExecutionBudget"), ("costs", "TestMission"), ("discovery", "."), ("publishedfiles", "."), ("safeundo", "."), ("learning", "TestNotebook")]
    for suite, pattern in suites:
        subprocess.run([f"/validation/{suite}.test", "-test.count=1", "-test.timeout=60s", "-test.run=" + pattern], check=True)
        check("Linux compiled " + suite + " suite", True)

    fixture = ThreadingHTTPServer(("127.0.0.1", 18991), FixtureProvider)
    threading.Thread(target=fixture.serve_forever, daemon=True).start()
    resources = [ThreadingHTTPServer(("127.0.0.1", port), FixtureChanges) for port in (18992, 18993)]
    for resource in resources:
        threading.Thread(target=resource.serve_forever, daemon=True).start()
    process = None
    with tempfile.TemporaryDirectory(prefix="soulacy-validation-") as root:
        workspace = Path(root)
        published = workspace / "published"
        published.mkdir()
        (published / "diagram.mmd").write_text("flowchart LR\n A[Server] --> B[Phone]\n")
        (published / ".env").write_text("hidden-fixture-must-not-be-returned")
        (workspace / "outside.txt").write_text("outside-fixture-must-not-be-returned")
        (published / "escape.txt").symlink_to(workspace / "outside.txt")
        config_path = workspace / "config.yaml"
        config_path.write_text(json.dumps({
            "server": {"host": "127.0.0.1", "port": 18789, "api_key": TOKEN,
                       "published_files": [{"agent_id": "validation-agent", "root": str(published)}],
                       "safe_undo": {"resources": [
                           {"id": "customer", "agent_id": "validation-agent", "name": "Customer", "kind": "json_record", "url": "http://127.0.0.1:18992/record", "fields": ["status"], "conditional_writes": True, "allow_loopback_http": True},
                           {"id": "document", "agent_id": "validation-agent", "name": "Document", "kind": "webdav_text", "url": "http://127.0.0.1:18993/document", "conditional_writes": True, "allow_loopback_http": True}]}},
            "llm": {"default_provider": "openai", "providers": {"openai": {"base_url": "http://127.0.0.1:18991/v1", "api_key": "fixture", "model": "validation-model"}}},
            "costs": {"enforcement_mode": "hard", "unknown_pricing": "block", "pricing": {"openai/validation-model": {"input_per_mtok": 1, "output_per_mtok": 2}}},
            "rate_limit": {"enabled": False},
            "agent_dirs": [str(workspace / "agents")],
            "runtime": {"timeouts": {"tool": "5s", "llm": "10s", "step": "15s", "run": "60s", "http": "70s"}},
        }))
        env = {**os.environ, "SOULACY_CONFIG_PATH": str(config_path), "SOULACY_WORKSPACE": str(workspace)}
        with (workspace / "gateway.log").open("w+") as log:
            try:
                process = start_gateway(env, log)
                request("GET", "/api/v1/autopilot/summary", status=401, authenticated=False)
                check("HTTP authentication is enforced", True)
                check("embedded web app serves", "<html" in request("GET", "/"))
                agent = {"id": "validation-agent", "name": "Validation", "trigger": "channel", "channels": ["http"], "enabled": True,
                         "system_prompt": "Return the test result.", "llm": {"provider": "openai", "model": "validation-model"}, "builtins": [], "learning": {"enabled": True},
                         "mission": {"id": "validation-mission", "goal": "Return verified output", "acceptance": [{"id": "evidence", "type": "output_contains", "value": "VERIFIED"}],
                                     "limits": {"max_cost_usd": 0.1, "max_duration": "30s", "allowed_tools": []}}}
                request("POST", "/api/v1/agents", agent, status=201)
                preparation_path = "/api/v1/agents/validation-agent/model-preparation"
                calls_before_preparation = FixtureProvider.calls
                request("GET", preparation_path, status=401, authenticated=False)
                preparation = request("GET", preparation_path)
                check("Model preparation resolves actual model without changing goals", preparation["goal_preserved"] and preparation["profile"]["model"] == "validation-model")
                check("Model preparation keeps unavailable capability evidence unknown", preparation["profile"]["native_tools"] == "unknown" and preparation["profile"]["source"] == "unknown")
                check("Model preparation leaves saved configuration intact", request("GET", "/api/v1/agents/validation-agent")["system_prompt"] == agent["system_prompt"])
                check("Model preparation preview performs no inference", FixtureProvider.calls == calls_before_preparation)
                undo_path, undo_token = safe_undo_checks()
                learned_path = learning_checks()
                files_path = "/api/v1/agents/validation-agent/files"
                request("GET", files_path, status=401, authenticated=False)
                files = request("GET", files_path)
                check("published folder hides dotfiles and symlinks", files["read_only"] and [e["name"] for e in files["entries"]] == ["diagram.mmd"])
                preview = request("GET", files_path + "/preview?path=diagram.mmd")
                check("published diagram is read-only UTF-8 text", preview["read_only"] and "Server" in preview["content"] and len(preview["sha256"]) == 64)
                request("GET", files_path + "/preview?path=..%2Foutside.txt", status=400)
                request("GET", files_path + "/preview?path=escape.txt", status=404)
                request("POST", files_path, {}, status=405)
                check("published HTTP boundary rejects traversal, links and writes", True)
                version = request("POST", "/api/v1/autopilot/deployments", {"agent_id": agent["id"], "version": "validation-v1"}, status=201)
                path = "/api/v1/autopilot/deployments/" + version["id"]
                request("POST", path + "/canary", {}, status=409)
                simulation = request("POST", path + "/simulate", {"input": "smoke scenario"})
                check("simulation has immutable proof and stage", simulation["proof"]["simulation"] and "state" in simulation)
                request("POST", path + "/canary", {}, status=409)
                check("simulation cannot satisfy live gates", True)
                qualification = None
                for _ in range(5):
                    qualification = request("POST", path + "/run", {"input": "qualification"})
                    check("real gateway run is verified and metered", qualification["proof"]["verification"] == "pass" and qualification["proof"].get("cost_usd", 0) > 0)
                request("POST", path + "/canary", {})
                request("POST", path + "/promote", {})
                check("ordered canary and stable promotion", True)

                body = {"agent_id": agent["id"], "session_id": "outbox-test", "text": "one turn"}
                first = request("POST", "/api/v1/chat", body, key="stable-offline-key")
                request("POST", "/api/v1/chat", body, key="stable-offline-key", status=409)
                check("HTTP idempotency cannot replay", first["run_id"] == "chat:stable-offline-key")

                failure = request("POST", path + "/run", {"input": "force-failure"})
                check("failed assertion is not successful", failure["proof"]["verification"] == "fail" and failure["proof"]["outcome"] == "failed")
                proposal = request("GET", "/api/v1/autopilot/proposals")["proposals"][0]
                request("POST", "/api/v1/autopilot/proposals/" + proposal["id"] + "/accept", {}, status=400)
                candidate = request("POST", path + "/run", {"input": "corrected run"})
                proposal_path = "/api/v1/autopilot/proposals/" + urllib.parse.quote(proposal["id"], safe="")
                verified = request("POST", proposal_path + "/verify", {"candidate_proof_id": candidate["proof"]["id"]})
                check("regression uses server-recorded baseline/candidate", verified["baseline"]["status"] == "fail" and verified["candidate"]["status"] == "pass")
                request("POST", proposal_path + "/accept", {"reason": "validation review"})

                goal = request("POST", "/api/v1/autopilot/goals", {"title": "Validation team", "objective": "Check dependency handoff", "budget": {"max_cost_usd": 0.2, "max_duration_ms": 60000},
                    "tasks": [{"id": "a", "title": "First", "prompt": "first", "agent_id": agent["id"], "depends_on": [], "budget": {"max_cost_usd": 0.1, "max_duration_ms": 20000}},
                              {"id": "b", "title": "Second", "prompt": "review", "agent_id": agent["id"], "depends_on": ["a"], "budget": {"max_cost_usd": 0.1, "max_duration_ms": 20000}}]}, status=201)
                goal_path = "/api/v1/autopilot/goals/" + goal["id"]
                request("POST", goal_path + "/run", {}, status=202)
                for _ in range(100):
                    state = request("GET", goal_path)
                    if state["status"] != "running":
                        break
                    time.sleep(0.1)
                check("goal completes with dependency proofs", state["status"] == "succeeded" and all(t.get("proof_id") for t in state["tasks"]))

                request("POST", "/api/v1/mobile/nodes/register", {"device_id": "validation-phone", "name": "Test fixture (not a real phone)", "platform": "ios", "capabilities": ["device.info"], "permissions": {}}, status=201)
                commands_path = "/api/v1/mobile/nodes/validation-phone/commands"
                command = request("POST", commands_path, {"command": "device.info", "params": {}}, status=201)
                claim_path = commands_path + "/" + command["id"] + "/claim"
                def claim(_):
                    try:
                        request("POST", claim_path, {})
                        return True
                    except AssertionError as exc:
                        if "got 409:" in str(exc):
                            return False
                        raise
                with ThreadPoolExecutor(max_workers=8) as pool:
                    winners = sum(pool.map(claim, range(8)))
                check("concurrent device claim has one winner", winners == 1)
                freeze_path = "/api/v1/autopilot/agents/validation-agent/freeze"
                request("POST", freeze_path, {"frozen": True, "reason": "restart safety test"})
                stop_gateway(process)
                process = None
                process = start_gateway(env, log)
                safe_undo_after_restart(undo_path, undo_token)
                learned = request("GET", learned_path)
                check("Learning approval and cited guidance survive process restart", learned["status"] == "active" and learned["content"] == "Use metric units in reports." and learned["citations"][0]["quote"] == "Use metric units in reports.")
                request("POST", "/api/v1/chat", {**body, "session_id": "frozen-after-restart"}, status=409)
                check("freeze survives process restart", True)
                check("claimed device command is never requeued", not request("GET", commands_path)["commands"])
                result_path = commands_path + "/" + command["id"] + "/result"
                result = {"status": "completed", "result": {"fixture": True}}
                request("POST", result_path, result)
                request("POST", result_path, result)
                request("POST", result_path, {"status": "completed", "result": {"changed": True}}, status=409)
                check("device result acknowledgements are exact-idempotent", True)
                request("POST", freeze_path, {"frozen": False, "reason": "test complete"})
                request("POST", "/api/v1/chat", body, key="stable-offline-key", status=409)
                check("run claim survives process restart", True)
                stop_gateway(process)
                process = None
            except Exception:
                log.flush()
                log.seek(0)
                print("GATEWAY DIAGNOSTICS (fixture credentials only):", log.read()[-12000:], flush=True)
                raise
            finally:
                if process is not None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                fixture.shutdown()
                for resource in resources:
                    resource.shutdown()
    print(json.dumps({"result": "passed", "checks": len(passed), "scope": "isolated Linux deployment; fixture provider; no physical device or real-provider validation"}), flush=True)


if __name__ == "__main__":
    main()
