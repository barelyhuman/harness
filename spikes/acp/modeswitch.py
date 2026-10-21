#!/usr/bin/env python3
"""Mid-session switching scenario: prompt, set_model, set_mode, set_config_option, prompt again."""
import sys, json, argparse, os
import spike
ap = argparse.Namespace(scenario="11-live-switch", cwd=sys.argv[1], perm="allow")
c = spike.Client(ap)
c.call("initialize", {"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":True,"writeTextFile":True},"terminal":True}})
c.call("authenticate", {"methodId":"cursor_login"})
sid = c.call("session/new", {"cwd": ap.cwd, "mcpServers": []})["result"]["sessionId"]
def p(t): print(t, json.dumps(c.call("session/prompt", {"sessionId":sid,"prompt":[{"type":"text","text":t}]}))[:200])
def call(m, **k): print(m, json.dumps(c.call(m, {"sessionId":sid, **k}))[:300])
call("session/set_model", modelId="composer-2.5[fast=true]")
p("Reply with just the word one.")
call("session/set_model", modelId="gemini-3.7-flash[effort=high]")   # mid-session, after a turn
p("Reply with just the word two.")
call("session/set_model", modelId="nonexistent-model")
call("session/set_mode", modeId="ask")
call("session/set_mode", modeId="plan")
call("session/set_mode", modeId="bogus")
call("session/set_config_option", configId="mode", value="agent")
call("session/set_config_option", configId="model", value="composer-2.5[fast=true]")
p("Reply with just the word three.")
call("session/list") if False else None
print("LIST", json.dumps(c.call("session/list", {"cwd": ap.cwd}))[:600])
print("SID", sid)
import time; time.sleep(1); c.p.terminate()
