#!/usr/bin/env python3
"""Pretty-print a fixture, skipping stream_event noise. show.py file [-s to include stream events] """
import json,sys
for l in open(sys.argv[1]):
    m=json.loads(l)
    if m['type']=='stream_event' and '-s' not in sys.argv: continue
    if m['type']=='system' and m.get('subtype')=='init': print('init', {k:v for k,v in m.items() if k not in('tools','slash_commands','skills','plugins','mcp_servers','agents')}); continue
    print(json.dumps(m)[:int(900)])
