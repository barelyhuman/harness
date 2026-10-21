#!/usr/bin/env python3
"""Spike driver: spawn claude with stream-json, script stdin, record fixtures.

Usage: drive.py <scenario> [--extra "flags"]
Scenarios are defined in SCENARIOS below as lists of steps:
  ("send", text)                       write a user turn
  ("raw", dict)                        write an arbitrary JSON line
  ("wait", predicate-name)             wait until a stdout message matches
  ("answer", fn)                       on control_request can_use_tool, reply using fn(req)->response body
  ("sleep", s)
Each scenario ends by closing stdin and waiting (or killing after timeout).
"""
import json, os, subprocess, sys, threading, time, queue

CWD = "/private/tmp/claude-501/-Users-sid-code-harness/e2689941-5abc-4789-a3a2-4db26225c942/scratchpad/claude-spike"
OUT = "/Users/sid/code/harness/internal/agent/claude/testdata"
BASE = ["claude", "--print", "--output-format", "stream-json", "--input-format", "stream-json",
        "--verbose", "--include-partial-messages", "--replay-user-messages", "--model", "haiku"]

def user(text):
    return {"type": "user", "message": {"role": "user", "content": text}}

class Run:
    def __init__(self, name, args, env=None):
        self.name = name
        self.p = subprocess.Popen(BASE + args, cwd=CWD, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, text=True, bufsize=1, env=env)
        self.q = queue.Queue()
        self.out = open(f"{OUT}/{name}.jsonl", "w")
        self.inp = open(f"{OUT}/{name}.stdin.jsonl", "w")
        self.err = []
        self.seen = []
        threading.Thread(target=self._rd, daemon=True).start()
        threading.Thread(target=lambda: [self.err.append(l) for l in self.p.stderr], daemon=True).start()

    def _rd(self):
        for line in self.p.stdout:
            self.out.write(line); self.out.flush()
            try: m = json.loads(line)
            except Exception: m = {"_raw": line}
            self.seen.append(m); self.q.put(m)
        self.q.put(None)

    def send(self, obj):
        s = json.dumps(obj)
        self.inp.write(s + "\n"); self.inp.flush()
        try:
            self.p.stdin.write(s + "\n"); self.p.stdin.flush()
        except BrokenPipeError:
            print("!! broken pipe on stdin")

    def wait(self, pred, timeout=120, handler=None):
        """Read messages until pred(m) true. handler(m) is called for every message."""
        end = time.time() + timeout
        while time.time() < end:
            try: m = self.q.get(timeout=1)
            except queue.Empty: continue
            if m is None: return None
            if handler: handler(m)
            if pred(m): return m
        print("!! timeout"); return None

    def close(self, wait=15):
        try: self.p.stdin.close()
        except Exception: pass
        try: self.p.wait(wait)
        except subprocess.TimeoutExpired:
            print("!! still alive after stdin close; killing"); self.p.kill()
        # drain
        time.sleep(0.3)
        self.out.close(); self.inp.close()
        print(f"[{self.name}] exit={self.p.returncode} stderr={''.join(self.err)[:500]!r}")

def is_result(m): return m.get("type") == "result"
def control_reply(r, req_id, response):
    r.send({"type": "control_response", "response": {"subtype": "success", "request_id": req_id, "response": response}})
