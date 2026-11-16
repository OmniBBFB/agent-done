"""Identify the real terminal window from a child process during acceptance."""

import json
import os
from pathlib import Path
import sys
import time

from agent_done.process import start_ticks
from agent_done.ipc import request


try:
    result = request({"op": "identify", "pid": os.getpid()}, timeout=15)["identity"]
    result.update(pid=os.getpid(), start_ticks=start_ticks(os.getpid()), term=os.environ.get('TERM'))
except (OSError, RuntimeError) as error:
    result = {"error": str(error)}
path = Path(sys.argv[1])
temporary = path.with_suffix('.tmp')
temporary.write_text(json.dumps(result))
temporary.replace(path)
if 'error' in result:
    raise SystemExit(1)
time.sleep(120)
