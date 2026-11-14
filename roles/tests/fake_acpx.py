"""A stand-in for `acpx --format json --json-strict ... exec --file -`, for the adapter's acp tests: it reads the prompt on
stdin and prints canned ACP JSON-RPC messages, one per line, the way acpx relays them. No agent, no network.

FAKE_ACPX selects the scenario:
  ok         a builder turn: writes done.txt in its cwd, answers the adapter's JSON object, reports USD 0.0123
  no_cost    the same turn with a usage_update that carries tokens and no cost
  refusal    session/prompt answers stopReason "refusal"
  error      session/prompt answers a JSON-RPC error and acpx exits 1
  budget     reports a growing cumulative cost (USD 0.30, 0.60, 0.90, ...) every 0.2 s and never ends its turn
  store_cut  records three finished steps of USD 0.01 in an OpenCode-like sqlite under HOME, sends no usage_update and
             sleeps past the cap
  store_budget  the same, one step of USD 0.01 every 0.2 s, never ending its turn
FAKE_ACPX_ARGV, if set, is a file where the fake writes its argv as JSON.
"""
from __future__ import annotations

import json
import os
import sqlite3
import sys
import time

SESSION = "fake-session"


def emit(msg: dict) -> None:
    print(json.dumps(msg), flush=True)


def update(u: dict) -> None:
    emit({"jsonrpc": "2.0", "method": "session/update", "params": {"sessionId": SESSION, "update": u}})


def main() -> int:
    if os.environ.get("FAKE_ACPX_ARGV"):
        with open(os.environ["FAKE_ACPX_ARGV"], "w", encoding="utf-8") as f:
            json.dump(sys.argv[1:], f)
    prompt = sys.stdin.read()
    scenario = os.environ.get("FAKE_ACPX", "ok")
    emit({"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
          "params": {"sessionId": SESSION, "prompt": [{"type": "text", "text": prompt[:80]}]}})
    if scenario == "budget":
        usd = 0.0
        while True:
            usd += 0.3
            update({"sessionUpdate": "usage_update", "used": 1000, "size": 200000,
                    "cost": {"amount": round(usd, 2), "currency": "USD"}})
            time.sleep(0.2)
    if scenario in ("store_cut", "store_budget"):
        # like OpenCode: each finished step's USD goes to its sqlite under HOME, usage_update would come only at turn end
        db = os.path.join(os.environ["HOME"], ".local", "share", "opencode", "opencode.db")
        os.makedirs(os.path.dirname(db), exist_ok=True)
        con = sqlite3.connect(db, isolation_level=None)
        con.execute("CREATE TABLE part (id TEXT, message_id TEXT, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT)")
        old = int(time.time() * 1000) - 3_600_000  # a step from before this call: never counted
        con.execute("INSERT INTO part VALUES ('p0', 'm0', 's0', ?, ?, ?)", (old, old, json.dumps({"type": "step-finish", "cost": 5})))
        update({"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read a.py", "kind": "read", "status": "pending"})
        for i in range(1, 1_000_000):
            now = int(time.time() * 1000)
            for kind in ("step-start", "text", "step-finish"):
                con.execute("INSERT INTO part VALUES (?, 'm1', 's1', ?, ?, ?)",
                            (f"p{i}{kind}", now, now, json.dumps({"type": kind, "cost": 0.01 if kind == "step-finish" else None})))
            if scenario == "store_cut" and i == 3:
                break
            time.sleep(0.2)
        time.sleep(3600)
    if scenario == "error":
        emit({"jsonrpc": "2.0", "id": 2, "error": {"code": -32603, "message": "the agent crashed"}})
        print("[acpx] error: AGENT_ERROR the agent crashed", file=sys.stderr)
        return 1
    if scenario == "refusal":
        emit({"jsonrpc": "2.0", "id": 2, "result": {"stopReason": "refusal"}})
        return 0
    update({"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Write done.txt", "kind": "edit", "status": "pending"})
    with open("done.txt", "w", encoding="utf-8") as f:
        f.write("done\n")
    update({"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "completed"})
    answer = json.dumps({"status": "ok", "reason": None, "files": [],
                         "payload": {"summary": "wrote done.txt", "files_changed": ["done.txt"]}})
    # the answer arrives in two chunks, as a streaming agent sends it
    update({"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": answer[:20]}})
    update({"sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": answer[20:]}})
    usage = {"sessionUpdate": "usage_update", "used": 5300, "size": 200000}
    if scenario == "ok":
        usage["cost"] = {"amount": 0.0123, "currency": "USD"}
    update(usage)
    emit({"jsonrpc": "2.0", "id": 2, "result": {"stopReason": "end_turn"}})
    return 0


if __name__ == "__main__":
    sys.exit(main())
