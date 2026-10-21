#!/usr/bin/env python3
"""basic + multiturn: two turns on one open stdin; observe lifetime after result."""
import sys, time; sys.path.insert(0, __import__("os").path.dirname(__file__))
from drive import *
r = Run("multiturn", ["--permission-mode", "default"])
r.send(user("Read README.md with the Read tool and tell me what it says in one short sentence."))
# auto-handle any control_request (allow) in case
def h(m):
    if m.get("type") == "control_request": print("CTRL", m)
m = r.wait(is_result, handler=h); print("turn1 result", m and m.get("subtype"))
print("alive after result?", r.p.poll() is None)
time.sleep(5); print("alive 5s after result?", r.p.poll() is None)
r.send(user("What was the first word of the file? One word."))
m = r.wait(is_result, handler=h); print("turn2 result", m and m.get("result"))
r.close()
