"""Identify the real terminal window from a child process during acceptance."""

import json
import os
from pathlib import Path
import sys
import time

from agent_done.desktop import start_ticks, window_identity


result = window_identity()
result.update(pid=os.getpid(), start_ticks=start_ticks(os.getpid()), term=os.environ.get('TERM'))
Path(sys.argv[1]).write_text(json.dumps(result))
time.sleep(120)
