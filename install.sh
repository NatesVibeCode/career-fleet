#!/usr/bin/env bash
set -euo pipefail
REPOSITORY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
python3 -m venv "$REPOSITORY/.venv"
"$REPOSITORY/.venv/bin/python" -m pip install "$REPOSITORY"
"$REPOSITORY/.venv/bin/career-fleet" build
printf '%s\n' 'Installed Career Fleet. Create an explicit request with: career-fleet init --objective "your research objective"'
