#!/usr/bin/env python3
"""permission_allow / permission_deny via --permission-prompt-tool stdio.
Usage: s_perm.py allow|deny [response-variant]"""
import sys, time, json, os; sys.path.insert(0, os.path.dirname(__file__))
from drive import *
mode = sys.argv[1]; variant = sys.argv[2] if len(sys.argv) > 2 else "std"
name = f"permission_{mode}" + ("" if variant == "std" else "_" + variant)
for f in ("hello.txt",):
    try: os.remove(f"{CWD}/{f}")
    except FileNotFoundError: pass
r = Run(name, ["--permission-mode", "default", "--permission-prompt-tool", "stdio"])
r.send(user("Create a file hello.txt containing the word hi using the Write tool. Do not use Bash."))
def h(m):
    if m.get("type") == "control_request":
        print("CTRL_REQ", json.dumps(m))
        req = m["request"]
        if req.get("subtype") == "can_use_tool":
            if mode == "allow":
                body = {"behavior": "allow", "updatedInput": req["input"]}
            else:
                body = {"behavior": "deny", "message": "User denied this action"}
            if variant == "noinput": body.pop("updatedInput", None)
            control_reply(r, m["request_id"], body)
m = r.wait(is_result, handler=h)
print("RESULT", json.dumps(m)[:800])
print("file exists:", os.path.exists(f"{CWD}/hello.txt"))
r.close()
