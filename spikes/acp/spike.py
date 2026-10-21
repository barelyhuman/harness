#!/usr/bin/env python3
"""ACP spike client for `agent acp` (Cursor). Usage:
  spike.py SCENARIO [--cwd DIR] [--model ID] [--perm allow|reject|first] [--prompt TEXT]...
           [--cancel-after SECS] [--set-model ID] [--set-mode ID] [--load SESSION_ID]
           [--advertise-terminal/--no-terminal] [--no-fs]
Writes every line (dir in/out) to ../../internal/agent/acp/testdata/SCENARIO.jsonl
"""
import argparse, json, os, subprocess, sys, threading, time, queue, pathlib

HERE = pathlib.Path(__file__).resolve().parent
TESTDATA = HERE.parent.parent / "internal/agent/acp/testdata"

class Client:
    def __init__(self, a):
        self.a = a
        TESTDATA.mkdir(parents=True, exist_ok=True)
        self.log = open(TESTDATA / f"{a.scenario}.jsonl", "w")
        self.p = subprocess.Popen([os.path.expanduser("~/.local/bin/agent"), "acp"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1, cwd=a.cwd)
        self.next_id = 1
        self.pending = {}
        self.lock = threading.Lock()
        self.terms = {}
        self.updates = []
        threading.Thread(target=self.reader, daemon=True).start()
        threading.Thread(target=self.errreader, daemon=True).start()

    def rec(self, d, m):
        with self.lock:
            self.log.write(json.dumps({"dir": d, "msg": m, "t": round(time.time(), 3)}) + "\n"); self.log.flush()

    def errreader(self):
        for l in self.p.stderr:
            self.rec("stderr", l.rstrip())

    def send(self, m):
        self.rec("out", m)
        self.p.stdin.write(json.dumps(m) + "\n"); self.p.stdin.flush()

    def call(self, method, params, timeout=300):
        with self.lock:
            i = self.next_id; self.next_id += 1
            q = queue.Queue(); self.pending[i] = q
        self.send({"jsonrpc": "2.0", "id": i, "method": method, "params": params})
        return q.get(timeout=timeout)

    def notify(self, method, params):
        self.send({"jsonrpc": "2.0", "method": method, "params": params})

    def reader(self):
        for l in self.p.stdout:
            l = l.strip()
            if not l: continue
            try: m = json.loads(l)
            except Exception: self.rec("in", {"raw": l}); continue
            self.rec("in", m)
            if "method" in m and "id" in m:
                threading.Thread(target=self.handle_req, args=(m,), daemon=True).start()
            elif "method" in m:
                self.updates.append(m)
                u = m.get("params", {}).get("update", {})
                print("update:", u.get("sessionUpdate"), json.dumps(u)[:200], file=sys.stderr)
            elif "id" in m and m["id"] in self.pending:
                self.pending.pop(m["id"]).put(m)

    def reply(self, m, result=None, error=None):
        r = {"jsonrpc": "2.0", "id": m["id"]}
        if error: r["error"] = error
        else: r["result"] = result
        self.send(r)

    def handle_req(self, m):
        meth, p = m["method"], m.get("params", {})
        print("agent->client request:", meth, json.dumps(p)[:300], file=sys.stderr)
        try:
            if meth == "fs/read_text_file":
                txt = open(p["path"]).read()
                if p.get("line"):
                    ls = txt.splitlines(True); s = p["line"] - 1
                    txt = "".join(ls[s: s + p["limit"]] if p.get("limit") else ls[s:])
                self.reply(m, {"content": txt})
            elif meth == "fs/write_text_file":
                open(p["path"], "w").write(p["content"]); self.reply(m, {})
            elif meth == "session/request_permission":
                opts = p["options"]
                pol = self.a.perm
                def pick(kinds):
                    for o in opts:
                        if o.get("kind") in kinds: return o
                    return opts[0]
                if pol == "reject": o = pick(["reject_once", "reject_always"])
                elif pol == "always": o = pick(["allow_always"])
                elif pol == "cancelled":
                    self.reply(m, {"outcome": {"outcome": "cancelled"}}); return
                elif pol == "first": o = opts[0]
                else: o = pick(["allow_once", "allow_always"])
                self.reply(m, {"outcome": {"outcome": "selected", "optionId": o["optionId"]}})
            elif meth == "cursor/create_plan":
                self.reply(m, {"outcome": {"outcome": "accepted"}})
            elif meth == "cursor/ask_question":
                self.reply(m, {"outcome": {"outcome": "skipped", "reason": "spike"}})
            elif meth in ("cursor/update_todos", "cursor/task", "cursor/generate_image"):
                self.reply(m, {})
            elif meth == "terminal/create":
                tid = f"t{len(self.terms)+1}"
                env = dict(os.environ); env.update({e["name"]: e["value"] for e in p.get("env", [])})
                pr = subprocess.Popen([p["command"], *p.get("args", [])], cwd=p.get("cwd"), env=env,
                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
                t = {"p": pr, "out": b""}
                def pump():
                    for chunk in iter(lambda: pr.stdout.read1(4096), b""): t["out"] += chunk
                threading.Thread(target=pump, daemon=True).start()
                t["th"] = None
                self.terms[tid] = t; self.reply(m, {"terminalId": tid})
            elif meth == "terminal/output":
                t = self.terms[p["terminalId"]]; rc = t["p"].poll()
                r = {"output": t["out"].decode(errors="replace"), "truncated": False}
                if rc is not None: r["exitStatus"] = {"exitCode": rc}
                self.reply(m, r)
            elif meth == "terminal/wait_for_exit":
                t = self.terms[p["terminalId"]]; rc = t["p"].wait(); time.sleep(.1)
                self.reply(m, {"exitCode": rc})
            elif meth == "terminal/kill":
                self.terms[p["terminalId"]]["p"].kill(); self.reply(m, {})
            elif meth == "terminal/release":
                self.reply(m, {})
            else:
                self.reply(m, error={"code": -32601, "message": "method not found"})
        except Exception as e:
            self.reply(m, error={"code": -32603, "message": str(e)})

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("scenario"); ap.add_argument("--cwd", default=os.environ.get("ACP_CWD"))
    ap.add_argument("--perm", default="allow"); ap.add_argument("--prompt", action="append", default=[])
    ap.add_argument("--cancel-after", type=float); ap.add_argument("--set-model"); ap.add_argument("--set-mode")
    ap.add_argument("--load"); ap.add_argument("--no-terminal", action="store_true"); ap.add_argument("--no-fs", action="store_true")
    ap.add_argument("--stop-after-init", action="store_true")
    ap.add_argument("--set-config", action="append", default=[], help="configId=value")
    a = ap.parse_args()
    a.cwd = os.path.abspath(a.cwd)
    c = Client(a)
    caps = {"fs": {"readTextFile": not a.no_fs, "writeTextFile": not a.no_fs}, "terminal": not a.no_terminal}
    r = c.call("initialize", {"protocolVersion": 1, "clientCapabilities": caps,
                              "clientInfo": {"name": "harness-spike", "version": "0.0.1"}})
    print(json.dumps(r, indent=1))
    ar = c.call('authenticate', {'methodId': 'cursor_login'}); print('AUTH', json.dumps(ar))
    if a.stop_after_init: return
    if a.load:
        r = c.call("session/load", {"sessionId": a.load, "cwd": a.cwd, "mcpServers": []}); sid = a.load
    else:
        r = c.call("session/new", {"cwd": a.cwd, "mcpServers": []}); sid = r.get("result", {}).get("sessionId")
    print(json.dumps(r, indent=1)[:3000])
    if a.set_model:
        print(json.dumps(c.call("session/set_model", {"sessionId": sid, "modelId": a.set_model})))
    if a.set_mode:
        print(json.dumps(c.call("session/set_mode", {"sessionId": sid, "modeId": a.set_mode})))
    for kv in a.set_config:
        k, v = kv.split("=", 1)
        print(json.dumps(c.call("session/set_config_option", {"sessionId": sid, "configId": k, "value": v})))
    for pr in a.prompt:
        if a.cancel_after:
            def cancel():
                time.sleep(a.cancel_after); c.notify("session/cancel", {"sessionId": sid})
            threading.Thread(target=cancel, daemon=True).start()
        r = c.call("session/prompt", {"sessionId": sid, "prompt": [{"type": "text", "text": pr}]})
        print("PROMPT RESULT", json.dumps(r))
    time.sleep(1)
    c.p.terminate()

if __name__ == "__main__": main()
