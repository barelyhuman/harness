#!/usr/bin/env python3
"""extras: always_allow (updatedPermissions) and interrupt_pending (interrupt while can_use_tool outstanding)."""
import sys, time, json, os; sys.path.insert(0, os.path.dirname(__file__))
from drive import *
which = sys.argv[1]
if which == "always_allow":
    r = Run("permission_always_allow", ["--permission-mode", "default", "--permission-prompt-tool", "stdio"])
    n = [0]
    def h(m):
        if m.get("type") == "control_request":
            n[0] += 1; print("CTRL_REQ #", n[0], m["request"]["tool_name"])
            control_reply(r, m["request_id"], {"behavior": "allow", "updatedInput": m["request"]["input"],
                "updatedPermissions": [{"type": "addRules", "rules": [{"toolName": "Write"}], "behavior": "allow", "destination": "session"}]})
    r.send(user("Using the Write tool, create a.txt with 'a', then b.txt with 'b'. Do not use Bash."))
    r.wait(is_result, handler=h)
    print("total can_use_tool requests:", n[0]); r.close()
else:
    r = Run("interrupt_pending", ["--permission-mode", "default", "--permission-prompt-tool", "stdio"])
    r.send(user("Using the Write tool, create c.txt with 'c'."))
    m = r.wait(lambda m: m.get("type") == "control_request")
    print("pending:", m["request_id"])
    r.send({"type": "control_request", "request_id": "i1", "request": {"subtype": "interrupt"}})
    def h(m): print("  ", m.get("type"), m.get("subtype"), json.dumps(m)[:200] if m.get("type") in ("control_response","control_cancel_request") else "")
    m = r.wait(is_result, handler=h, timeout=30)
    print("alive?", r.p.poll() is None)
    r.close()
