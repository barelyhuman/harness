#!/usr/bin/env python3
"""plan_mode and bash scenarios. Usage: s_plan_bash.py plan|bash"""
import sys, time, json, os; sys.path.insert(0, os.path.dirname(__file__))
from drive import *
which = sys.argv[1]
def h_factory(r, plan_answer=None):
    def h(m):
        if m.get("type") == "control_request":
            print("CTRL_REQ", json.dumps(m)[:1500])
            req = m["request"]
            if req.get("subtype") == "can_use_tool":
                if plan_answer and req["tool_name"] == "ExitPlanMode":
                    control_reply(r, m["request_id"], plan_answer(req))
                else:
                    control_reply(r, m["request_id"], {"behavior": "allow", "updatedInput": req["input"]})
    return h
if which == "plan":
    r = Run("plan_mode", ["--permission-mode", "plan", "--permission-prompt-tool", "stdio"])
    r.send(user("Plan how to add a CONTRIBUTING.md file to this repo. When your plan is ready, present it with ExitPlanMode."))
    m = r.wait(is_result, handler=h_factory(r, lambda req: {"behavior": "allow", "updatedInput": req["input"]}), timeout=180)
    print("RESULT", m and m.get("result", "")[:300])
    r.send({"type":"control_request","request_id":"pm","request":{"subtype":"get_plan"}})
    r.wait(lambda m: m.get("type")=="control_response" and m["response"]["request_id"]=="pm", timeout=10, handler=lambda m: print("get_plan", json.dumps(m)[:500]))
    r.close()
else:
    r = Run("bash", ["--permission-mode", "default", "--permission-prompt-tool", "stdio"])
    r.send(user("Use the Bash tool to run: for i in 1 2 3; do echo line$i; sleep 2; done  -- then tell me what it printed."))
    t0 = time.time()
    def h(m):
        print(f"{time.time()-t0:5.1f}s", m.get("type"), m.get("subtype", ""), json.dumps(m)[:140] if m.get("type") != "stream_event" else m["event"]["type"])
        h2(m)
    h2 = h_factory(r)
    m = r.wait(is_result, handler=h, timeout=120)
    r.close()
