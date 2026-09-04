#!/usr/bin/env python3
"""Reference External Storage Protocol v1 sidecar (Story E24).

A dependency-free, in-memory vector + queue backend speaking JSON-RPC 2.0
over stdio (one message per line). It exists to prove the contract is
implementable outside Go and to anchor the conformance kit
(sdk/extstorage/storagetest) in CI.

"Similarity" here is a trivial token-overlap score — real sidecars bring
their own embeddings/databases. Large content may arrive as a file
reference (content_file) relative to the negotiated shared directory.
"""

import json
import sys
import threading

PROTOCOL_VERSION = 1


class State:
    def __init__(self):
        self.shared_dir = ""
        self.entries = []  # vector store: list of dicts
        self.storage_entries = [] # memory archive: list of dicts
        self.subs = {}     # subscription_id -> {"subject": ..., "group": ...}
        self.next_sub = 0
        self.lock = threading.Lock()


STATE = State()


def reply(msg_id, result=None, error=None):
    out = {"jsonrpc": "2.0", "id": msg_id}
    if error is not None:
        out["error"] = error
    else:
        out["result"] = result if result is not None else {}
    sys.stdout.write(json.dumps(out) + "\n")
    sys.stdout.flush()


def notify(method, params):
    sys.stdout.write(json.dumps(
        {"jsonrpc": "2.0", "method": method, "params": params}) + "\n")
    sys.stdout.flush()


def err(code, message):
    return {"code": code, "message": message}


def score(query, content):
    q = set(query.lower().split())
    c = set(content.lower().split())
    if not q or not c:
        return 0.0
    return len(q & c) / float(len(q | c))


def resolve_content(params):
    content = params.get("content", "")
    rel = params.get("content_file", "")
    if rel and STATE.shared_dir:
        import os
        path = os.path.normpath(os.path.join(STATE.shared_dir, rel))
        if not path.startswith(os.path.normpath(STATE.shared_dir)):
            raise ValueError("content_file escapes shared dir")
        with open(path, "r", encoding="utf-8") as f:
            content = f.read()
    return content


def handle(msg):
    method = msg.get("method", "")
    msg_id = msg.get("id")
    params = msg.get("params") or {}

    if method == "negotiate":
        proto = params.get("protocol", 0)
        if proto < 1:
            reply(msg_id, error=err(-32602, "missing protocol"))
            return True
        STATE.shared_dir = params.get("shared_dir", "")
        reply(msg_id, result={
            "protocol": min(proto, PROTOCOL_VERSION),
            "name": "reference-storage-sidecar",
            "capabilities": ["vector", "queue", "storage"],
            "shared_dir": STATE.shared_dir,
        })
        return True

    if method == "shutdown":
        if msg_id is not None:
            reply(msg_id, result={"ok": True})
        return False

    if method == "vector.write":
        try:
            content = resolve_content(params)
        except Exception as e:  # noqa: BLE001 - reported over the wire
            reply(msg_id, error=err(-32000, "content_file: %s" % e))
            return True
        with STATE.lock:
            STATE.entries.append({
                "id": params.get("id", ""),
                "agent_id": params.get("agent_id", ""),
                "session_id": params.get("session_id", ""),
                "scope": params.get("scope", ""),
                "content": content,
                "timestamp": params.get("timestamp", 0),
            })
        reply(msg_id, result={"ok": True})
        return True

    if method == "vector.search":
        agent = params.get("agent_id", "")
        query = params.get("query", "")
        top_k = params.get("top_k", 5) or 5
        with STATE.lock:
            pool = [e for e in STATE.entries
                    if not agent or e["agent_id"] == agent]
        hits = sorted(pool, key=lambda e: -score(query, e["content"]))[:top_k]
        reply(msg_id, result={"results": [{
            "id": e["id"], "agent_id": e["agent_id"],
            "session_id": e["session_id"], "scope": e["scope"],
            "content": e["content"], "timestamp": e["timestamp"],
            "distance": 1.0 - score(query, e["content"]),
        } for e in hits]})
        return True

    if method == "storage.archive":
        try:
            entry = params.get("entry") or {}
            content = entry.get("content", "")
            rel = params.get("content_file", "")
            if rel and STATE.shared_dir:
                import os
                path = os.path.normpath(os.path.join(STATE.shared_dir, rel))
                if not path.startswith(os.path.normpath(STATE.shared_dir)):
                    raise ValueError("content_file escapes shared dir")
                with open(path, "r", encoding="utf-8") as f:
                    content = f.read()
                entry["content"] = content
        except Exception as e:
            reply(msg_id, error=err(-32000, "content_file: %s" % e))
            return True
        with STATE.lock:
            if not any(e.get("id") == entry.get("id") for e in STATE.storage_entries):
                STATE.storage_entries.append(entry)
        reply(msg_id, result={"ok": True})
        return True

    if method == "storage.search":
        agent = params.get("agent_id", "")
        query = params.get("query", "")
        limit = params.get("limit", 5) or 5
        with STATE.lock:
            pool = [e for e in STATE.storage_entries
                    if not agent or e.get("agent_id") == agent]
        hits = sorted(pool, key=lambda e: -score(query, e.get("content", "")))[:limit]
        reply(msg_id, result={"entries": hits})
        return True

    if method == "storage.read_by_scope":
        agent = params.get("agent_id", "")
        session = params.get("session_id", "")
        scope = params.get("scope", "")
        limit = params.get("limit", 5) or 5
        with STATE.lock:
            pool = [e for e in STATE.storage_entries
                    if e.get("agent_id") == agent and
                       e.get("session_id") == session and
                       e.get("scope") == scope]
        pool = sorted(pool, key=lambda e: e.get("created_at", ""), reverse=True)[:limit]
        reply(msg_id, result={"entries": pool})
        return True

    if method == "storage.read_global":
        agent = params.get("agent_id", "")
        limit = params.get("limit", 5) or 5
        with STATE.lock:
            pool = [e for e in STATE.storage_entries
                    if e.get("agent_id") == agent]
        pool = sorted(pool, key=lambda e: e.get("created_at", ""), reverse=True)[:limit]
        reply(msg_id, result={"entries": pool})
        return True

    if method == "storage.prune":
        agent = params.get("agent_id", "")
        before_str = params.get("before", "")
        deleted = 0
        with STATE.lock:
            new_entries = []
            for e in STATE.storage_entries:
                if e.get("agent_id") == agent and e.get("created_at", "") < before_str:
                    deleted += 1
                else:
                    new_entries.append(e)
            STATE.storage_entries = new_entries
        reply(msg_id, result={"rows_deleted": deleted})
        return True

    if method == "queue.publish":
        subject = params.get("subject", "")
        with STATE.lock:
            targets = [sid for sid, s in STATE.subs.items()
                       if subject_matches(s["subject"], subject)]
        for sid in targets:
            notify("queue.message", {
                "subscription_id": sid, "subject": subject,
                "data": params.get("data", ""),
            })
        reply(msg_id, result={"ok": True})
        return True

    if method == "queue.subscribe":
        with STATE.lock:
            STATE.next_sub += 1
            sid = "sub-%d" % STATE.next_sub
            STATE.subs[sid] = {"subject": params.get("subject", ">"),
                               "group": params.get("group", "")}
        reply(msg_id, result={"subscription_id": sid})
        return True

    if method == "queue.unsubscribe":
        with STATE.lock:
            STATE.subs.pop(params.get("subscription_id", ""), None)
        reply(msg_id, result={"ok": True})
        return True

    if method == "queue.ack":
        reply(msg_id, result={"ok": True})  # in-memory: nothing to do
        return True

    if msg_id is not None:
        reply(msg_id, error=err(-32601, "method not found: %s" % method))
    # Unknown notifications are skipped silently (forward compat).
    return True


def subject_matches(pattern, subject):
    """NATS-style matching: '*' = one token, '>' = trailing tokens."""
    pt, st = pattern.split("."), subject.split(".")
    for i, p in enumerate(pt):
        if p == ">":
            return True
        if i >= len(st):
            return False
        if p != "*" and p != st[i]:
            return False
    return len(pt) == len(st)


def main():
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            msg = json.loads(line)
        except ValueError:
            continue  # malformed lines must never kill the process
        if not isinstance(msg, dict) or msg.get("jsonrpc") != "2.0":
            continue
        try:
            if not handle(msg):
                return
        except Exception as e:  # noqa: BLE001 - belt and braces
            if msg.get("id") is not None:
                reply(msg["id"], error=err(-32603, "internal: %s" % e))


if __name__ == "__main__":
    main()
