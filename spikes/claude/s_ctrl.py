#!/usr/bin/env python3
"""set_model, set_permission_mode, initialize, list_models, interrupt. Usage: s_ctrl.py set_model|interrupt|misc"""
import sys, time, json, os; sys.path.insert(0, os.path.dirname(__file__))
from drive import *
which = sys.argv[1]
def creq(r, rid, req):
    r.send({"type": "control_request", "request_id": rid, "request": req})
def wait_resp(r, rid, t=20):
    return r.wait(lambda m: m.get("type") == "control_response" and m["response"].get("request_id") == rid, timeout=t)
def autoallow(r):
    def h(m):
        if m.get("type") == "control_request" and m["request"].get("subtype") == "can_use_tool":
            control_reply(r, m["request_id"], {"behavior": "allow", "updatedInput": m["request"]["input"]})
    return h

if which == "set_model":
    r = Run("set_model", ["--permission-mode", "default"])
    r.send(user("Say the word one."))
    r.wait(is_result)
    creq(r, "m1", {"subtype": "set_model", "model": "sonnet"})
    print("set_model resp:", json.dumps(wait_resp(r, "m1")))
    r.send(user("Say the word two."))
    m = r.wait(lambda m: m.get("type") == "assistant" and any(b.get("type") == "text" for b in m["message"]["content"]))
    print("assistant model after switch:", m["message"]["model"])
    r.wait(is_result)
    # bad model
    creq(r, "m2", {"subtype": "set_model", "model": "no-such-model"})
    print("bad model resp:", json.dumps(wait_resp(r, "m2")))
    r.close()
elif which == "interrupt":
    r = Run("interrupt", ["--permission-mode", "default"])
    r.send(user("Write a very long essay (2000 words) about the history of tea."))
    r.wait(lambda m: m.get("type") == "stream_event" and m["event"].get("delta", {}).get("type") == "text_delta")
    time.sleep(1)
    creq(r, "i1", {"subtype": "interrupt"})
    print("interrupt resp:", json.dumps(wait_resp(r, "i1")))
    m = r.wait(is_result, timeout=30)
    print("result after interrupt:", json.dumps(m)[:600])
    print("alive?", r.p.poll() is None)
    r.send(user("Say the word ok."))
    m = r.wait(is_result, timeout=60)
    print("follow-up result:", m and m.get("result"))
    r.close()
elif which == "misc":
    r = Run("misc_control", ["--permission-mode", "default"])
    # no initialize first: requests before any user turn
    creq(r, "p1", {"subtype": "set_permission_mode", "mode": "acceptEdits"})
    print("set_permission_mode resp:", json.dumps(wait_resp(r, "p1")))
    creq(r, "p2", {"subtype": "set_permission_mode", "mode": "bogus"})
    print("bad mode resp:", json.dumps(wait_resp(r, "p2")))
    creq(r, "init1", {"subtype": "initialize"})
    resp = wait_resp(r, "init1"); print("initialize resp:", json.dumps(resp)[:1500])
    creq(r, "lm", {"subtype": "list_models"})
    print("list_models resp:", json.dumps(wait_resp(r, "lm"))[:1500])
    creq(r, "u", {"subtype": "get_context_usage"})
    print("get_context_usage:", json.dumps(wait_resp(r, "u"))[:600])
    creq(r, "x", {"subtype": "nonsense"})
    print("unknown subtype:", json.dumps(wait_resp(r, "x")))
    # confirm mode took effect: write w/o prompt?
    r.send(user("Create file mode.txt with 'x' using the Write tool."))
    r.wait(is_result, handler=lambda m: print("CTRL", m) if m.get("type") == "control_request" else None)
    r.close()
