#!/usr/bin/env python3
"""basic: a single turn with a read-only tool, then close stdin (exit 0)."""
import sys, os; sys.path.insert(0, os.path.dirname(__file__))
from drive import *
r = Run("basic", ["--permission-mode", "default"])
r.send(user("Read README.md with the Read tool and tell me what it says in one short sentence."))
r.wait(is_result); r.close()
