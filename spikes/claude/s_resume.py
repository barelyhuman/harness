#!/usr/bin/env python3
"""resume: run 1 sets a secret; process exits (stdin closed); run 2 uses --resume. Also bogus id."""
import sys, time, json, os, subprocess; sys.path.insert(0, os.path.dirname(__file__))
import drive
from drive import *
r = Run("resume_first", [])
r.send(user("Remember the secret word PINEAPPLE. Reply only OK."))
m = r.wait(is_result); sid = m["session_id"]; print("sid", sid)
r.close()
r = Run("resume", ["--resume", sid])
r.send(user("What was the secret word? One word."))
m = r.wait(is_result); print("resumed answer:", m and m.get("result"), "same sid?", m and m["session_id"] == sid)
init = [x for x in r.seen if x.get("subtype") == "init"]; print("init sid:", init and init[0]["session_id"])
r.close()
# bogus
r = Run("resume_bogus", ["--resume", "00000000-0000-4000-8000-000000000000"])
r.send(user("hi"))
time.sleep(5)
print("bogus: alive?", r.p.poll(), "seen:", [json.dumps(x)[:300] for x in r.seen])
r.close()
