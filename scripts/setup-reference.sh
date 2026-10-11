#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
python3 - <<'CHECK'
import hashlib
from pathlib import Path
expected = '88ec550698d968addfe4f9da87496b8bc57a84ef7bd07690b00901f92179d50a'
if hashlib.sha256(Path('cli-master.zip').read_bytes()).hexdigest() != expected:
    raise SystemExit('cli-master.zip does not match the documented baseline')
CHECK
mkdir -p .reference
if [ ! -d .reference/cli-master ]; then
    unzip -q cli-master.zip -d .reference
fi
if [ ! -x .reference/venv/bin/python ]; then
    uv venv .reference/venv --python 3.11.15
fi
uv pip install --python .reference/venv/bin/python -r scripts/reference-requirements.txt ./.reference/cli-master
