#!/usr/bin/env bash
#
# One-click installer for career-fleet (macOS / Linux).
#
# Creates a private Python environment in this repo's `.venv`, installs the
# career-fleet CLI into it, sets up a workspace with the bundled assistant
# skills, creates your profile, runs the offline demo, and registers the MCP
# tools with Claude Desktop (or Cursor).
#
# Usage:
#   ./install.sh [workspace-directory]
#
# The workspace defaults to ~/career-fleet-workspace when no argument is given.

set -euo pipefail

# Resolve this script's own directory so the installer works no matter which
# directory the caller starts in (and even when invoked via a symlink).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

VENV_DIR="$SCRIPT_DIR/.venv"
VENV_PY="$VENV_DIR/bin/python"
CLI="$VENV_DIR/bin/career-fleet"
LOG="$VENV_DIR/install.log"

# --- tiny, friendly output helpers ------------------------------------------

say()  { printf '%s\n' "$*"; }
ok()   { printf '  \342\234\223 %s\n' "$*"; }
warn() { printf '  \342\232\240 %s\n' "$*"; }
fail() {
  printf '  \342\234\227 %s\n' "$*" >&2
  exit 1
}

# --- 1. find a Python 3.10+ interpreter -------------------------------------

PYTHON_BIN=""

try_python() {
  # $1 = interpreter name or absolute path. Sets PYTHON_BIN and returns 0 when
  # the interpreter exists and is new enough.
  command -v "$1" >/dev/null 2>&1 || return 1
  "$1" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)' >/dev/null 2>&1 || return 1
  PYTHON_BIN="$1"
}

find_python() {
  local v dir
  try_python python3 && return 0
  for v in 14 13 12 11 10; do
    try_python "python3.$v" && return 0
  done
  # Homebrew and similar install locations are not always on PATH, so check
  # them explicitly (newest version first).
  for dir in /opt/homebrew/bin /usr/local/bin; do
    [ -d "$dir" ] || continue
    for v in 14 13 12 11 10; do
      try_python "$dir/python3.$v" && return 0
    done
  done
  return 1
}

say "career-fleet installer"
say ""
say "Step 1/8 — Finding Python 3.10+"
if ! find_python; then
  say ""
  say "  Your Python is too old or missing."
  say "  Install it with:  brew install python@3.13   (macOS)"
  say "  or download from: https://www.python.org/downloads/"
  say "  Then run this installer again."
  say ""
  exit 1
fi
ok "Using $PYTHON_BIN"

# --- 2. create/reuse the virtualenv and install the package ------------------

say ""
say "Step 2/8 — Preparing a private Python environment (.venv)"
# mkdir first so the log redirect below always has somewhere to write.
mkdir -p "$VENV_DIR"
if [ ! -x "$VENV_PY" ]; then
  "$PYTHON_BIN" -m venv "$VENV_DIR" >"$LOG" 2>&1 \
    || fail "Could not create the .venv environment. See $LOG for details."
fi

# pip upgrades are a nice-to-have and fail harmlessly offline; never block on it.
if "$VENV_PY" -m pip install --upgrade pip >>"$LOG" 2>&1; then
  :
else
  warn "Could not upgrade pip (offline?). Continuing with the bundled version."
fi

say "Installing career-fleet (this can take a minute the first time)…"
if ! ( cd "$SCRIPT_DIR" && "$VENV_PY" -m pip install . ) >>"$LOG" 2>&1; then
  fail "Installing career-fleet failed. See $LOG for details. (Usually a network problem reaching PyPI.)"
fi

if ! "$VENV_PY" -c 'import career_fleet, harness_fleet' >>"$LOG" 2>&1; then
  fail "career-fleet installed but could not be imported. See $LOG for details."
fi

# Web discovery (search, job boards, article extraction) lives in an optional
# extra. It needs to compile a few packages, so treat it as best-effort: the
# tool is fully usable without it, just without automatic sourcing.
say "Adding web discovery (optional)…"
if ( cd "$SCRIPT_DIR" && "$VENV_PY" -m pip install ".[discover]" ) >>"$LOG" 2>&1; then
  ok "Web discovery installed"
else
  warn "Could not install web discovery (offline?). Sourcing will be limited;"
  warn "add it later with: $VENV_PY -m pip install \"$SCRIPT_DIR[discover]\""
fi
ok "career-fleet installed at $CLI"

# --- 3. choose and create the workspace -------------------------------------

say ""
say "Step 3/8 — Creating the workspace"
WS="${1:-$HOME/career-fleet-workspace}"
mkdir -p "$WS" || fail "Could not create the workspace directory: $WS"
# Canonicalize to an absolute path. Every step below cds into the workspace, so a
# relative --db would otherwise resolve against the *new* working directory and
# land in a nested path.
WS="$(cd "$WS" && pwd)" || fail "Could not resolve the workspace directory: $WS"
ok "Workspace at $WS"

# Two files live here, by design: career_fleet.db holds your pipeline (companies,
# postings, lanes) and the shared engine keeps its own state — model routes, runs,
# and the demo — in harness-fleet.db.
CAREER_DB="$WS/career_fleet.db"
ENGINE_DB="$WS/harness-fleet.db"

# --- 4. install the bundled assistant skills --------------------------------

say ""
say "Step 4/8 — Installing the assistant skills"
if ! ( cd "$WS" && "$CLI" setup --workspace-root "$WS" ) >>"$LOG" 2>&1; then
  fail "Could not install the assistant skills. See $LOG for details."
fi
ok "Skills installed in $WS/.agents/skills"

# --- 5. create the database and your profile --------------------------------

say ""
say "Step 5/8 — Creating your profile"
if ! ( cd "$WS" && "$CLI" init --workspace-root "$WS" --db "$CAREER_DB" ) >>"$LOG" 2>&1; then
  fail "Could not initialize the workspace. See $LOG for details."
fi
ok "Profile created at $WS/profile.json — edit it to say what you want"

# --- 6. run the offline demo -------------------------------------------------

say ""
say "Step 6/8 — Running the offline demo (no API keys needed)"
DEMO_JSON="$WS/runs/demo-01/clean_packet.json"
DEMO_CSV="$WS/runs/demo-01/clean_packet.csv"
if [ -f "$DEMO_JSON" ] && [ -f "$DEMO_CSV" ]; then
  ok "Demo already present — skipping"
else
  # The demo scores six sample postings with a built-in fake model, so it proves
  # the install without an account, a key, or any spend.
  if ! ( cd "$WS" && "$VENV_PY" -m harness_fleet.cli quickstart --demo \
           --run-id demo-01 --db "$ENGINE_DB" ) >>"$LOG" 2>&1; then
    fail "The demo run failed. See $LOG for details."
  fi
fi
if [ ! -f "$DEMO_CSV" ]; then
  fail "The demo did not produce its expected output files. See $LOG for details."
fi
ok "Demo scored 6 sample postings"

# --- 7. register with Claude Desktop / Cursor --------------------------------

say ""
say "Step 7/8 — Registering with Claude Desktop / Cursor"
# Desktop apps do not inherit this shell's environment, so hand the caller's
# OpenRouter key to the MCP server when one is set (the CLI never prints values).
MCP_INSTALL_ARGS=(--workspace-root "$WS" --db "$ENGINE_DB")
if [ -n "${OPENROUTER_API_KEY:-}" ]; then
  MCP_INSTALL_ARGS+=(--env OPENROUTER_API_KEY)
fi
if "$VENV_PY" -m harness_fleet.cli mcp install "${MCP_INSTALL_ARGS[@]}" >>"$LOG" 2>&1; then
  ok "MCP server registered"
  if [ -n "${OPENROUTER_API_KEY:-}" ]; then
    say "  OPENROUTER_API_KEY passed into the client config (value not shown)"
  fi
else
  warn "Could not register with Claude Desktop / Cursor automatically."
  say "  Add this entry to your MCP client config manually, then restart the client:"
  cat <<JSON
{
  "mcpServers": {
    "career-fleet": {
      "command": "$VENV_PY",
      "args": ["-m", "harness_fleet.cli", "serve", "--workspace-root", "$WS", "--db", "$ENGINE_DB"]
    }
  }
}
JSON
fi

# --- 8. getting free model access ------------------------------------------

say ""
say "Step 8/8 — Getting free model access (optional)"
say "  Career Fleet's own lanes need no AI account at all — triage and recon run on rules"
say "  and the text of the postings. This only matters if you want the shared engine's"
say "  batch runs or the career-screening task."
if [ -n "${OPENROUTER_API_KEY:-}" ]; then
  ok "OpenRouter key detected — free OpenRouter models are ready for engine runs"
elif command -v opencode >/dev/null 2>&1; then
  ok "OpenCode is installed — sign in once if you have not: opencode auth login"
else
  say "  Optional: connect a free model for engine runs. Pick one:"
  say ""
  say "  1. OpenRouter — one signup:"
  say "       https://openrouter.ai/          (create the account)"
  say "       https://openrouter.ai/keys      (create a key, copy it)"
  say "     then:  export OPENROUTER_API_KEY=\"sk-or-...\""
  say ""
  say "  2. OpenCode — free hosted models:"
  say "       curl -fsSL https://opencode.ai/install | bash"
  say "       opencode auth login"
  say ""
  say "  3. Your own computer — no signup at all:  https://ollama.com/download"
  say ""
  say "  Full walkthrough: $SCRIPT_DIR/FREE-ACCESS.md"
fi

# --- 8. summary --------------------------------------------------------------

say ""
say "──────────────────────────────────────────────────────────────"
say "  career-fleet is ready!"
say "──────────────────────────────────────────────────────────────"
say ""
say "  Installed:"
say "    Python environment : $VENV_DIR"
say "    career-fleet CLI   : $CLI"
say "    Workspace          : $WS"
say "    Your profile       : $WS/profile.json"
say "    Demo results       : $DEMO_CSV"
say ""
say "  Try next:"
say "    1. Read the demo results (a spreadsheet):"
say "         $DEMO_CSV"
say "       Those six sample postings were scored by the engine, so they live in"
say "       $ENGINE_DB — your own pipeline below uses $CAREER_DB."
say "    2. Gather real companies:"
say "         $CLI discover --source yc --target W24 --max 30 --db \"$CAREER_DB\""
say "       then  $CLI triage --db \"$CAREER_DB\"   and   $CLI recon --db \"$CAREER_DB\""
say "       and browse what you gathered:  $CLI board --open --db \"$CAREER_DB\""
say "    3. Restart Claude Desktop (or Cursor) and just ask it to screen employers"
say "       for you — the bundled career-fleet skill runs these steps for you."
say ""
