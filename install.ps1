<#
.SYNOPSIS
  One-click installer for career-fleet (Windows).

.DESCRIPTION
  Creates a private Python environment in this repo's `.venv`, installs the
  career-fleet CLI into it, sets up a workspace with the bundled assistant
  skills, creates your profile, runs the offline demo, and registers the MCP
  tools with Claude Desktop (or Cursor).

  Usage:
    .\install.ps1 [workspace-directory]

  The workspace defaults to ~\career-fleet-workspace when no argument is given.

.NOTES
  If PowerShell script execution is blocked, run this instead:

    powershell -ExecutionPolicy Bypass -File install.ps1
#>

$ErrorActionPreference = "Stop"

# Directory containing this script, resolved to an absolute path so the
# installer works regardless of the caller's working directory.
$scriptDir = $PSScriptRoot
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $scriptDir) { $scriptDir = (Get-Location).Path }

$venvDir = Join-Path $scriptDir ".venv"
$venvPy  = Join-Path $venvDir "Scripts\python.exe"
$cli     = Join-Path $venvDir "Scripts\career-fleet.exe"
$log     = Join-Path $venvDir "install.log"

# --- tiny, friendly output helpers ------------------------------------------

function Say([string]$Text)  { Write-Host $Text }
function Ok([string]$Text)   { Write-Host "  [OK]  $Text" -ForegroundColor Green }
function Warn([string]$Text) { Write-Host "  [!]   $Text" -ForegroundColor Yellow }
function Fail([string]$Text) {
  Write-Host "  [X]   $Text" -ForegroundColor Red
  exit 1
}

# --- 1. find a Python 3.10+ interpreter -------------------------------------

$script:pythonExe = $null
$script:pythonArgs = @()

function Test-PythonCandidate {
  param(
    [Parameter(Mandatory = $true)][string]$Exe,
    [string[]]$VersionArgs = @()
  )
  $cmd = Get-Command $Exe -ErrorAction SilentlyContinue
  if (-not $cmd) { return $false }
  # The Microsoft Store ships a stub python.exe that opens the Store instead of
  # running Python; skip it so we never pop up a window mid-install.
  if ($cmd.Source -and $cmd.Source -like "*\WindowsApps\*") { return $false }
  $code = 'import sys; sys.exit(0 if sys.version_info >= (3,10) else 1)'
  try {
    & $Exe @VersionArgs -c $code *> $null
    return ($LASTEXITCODE -eq 0)
  } catch {
    return $false
  }
}

function Find-Python {
  # Prefer the `py` launcher (ships with python.org installs) and walk down from
  # the newest version; then fall back to a bare `python` / `python3` on PATH.
  $candidates = @(
    @{ Exe = "py"; Args = @("-3.14") },
    @{ Exe = "py"; Args = @("-3.13") },
    @{ Exe = "py"; Args = @("-3.12") },
    @{ Exe = "py"; Args = @("-3.11") },
    @{ Exe = "py"; Args = @("-3.10") },
    @{ Exe = "py"; Args = @() },
    @{ Exe = "python"; Args = @() },
    @{ Exe = "python3"; Args = @() }
  )
  foreach ($c in $candidates) {
    if (Test-PythonCandidate -Exe $c.Exe -VersionArgs @($c.Args)) {
      $script:pythonExe = $c.Exe
      $script:pythonArgs = @($c.Args)
      return $true
    }
  }
  return $false
}

Say "career-fleet installer"
Say ""
Say "Step 1/8 - Finding Python 3.10+"
if (-not (Find-Python)) {
  Say ""
  Say "  Your Python is too old or missing."
  Say "  Install it with:  winget install Python.Python.3.13   (Windows)"
  Say "  or download from: https://www.python.org/downloads/"
  Say "  Then run this installer again."
  Say ""
  exit 1
}
Ok "Using $pythonExe $($pythonArgs -join ' ')"

# --- 2. create/reuse the virtualenv and install the package ------------------

Say ""
Say "Step 2/8 - Preparing a private Python environment (.venv)"
# Create the directory first so the log redirect below always has somewhere to write.
New-Item -ItemType Directory -Force -Path $venvDir | Out-Null
if (-not (Test-Path $venvPy)) {
  & $pythonExe @pythonArgs -m venv $venvDir *> $log
  if ($LASTEXITCODE -ne 0) { Fail "Could not create the .venv environment. See $log for details." }
}

# pip upgrades are a nice-to-have and fail harmlessly offline; never block on it.
& $venvPy -m pip install --upgrade pip *>> $log
if ($LASTEXITCODE -ne 0) {
  Warn "Could not upgrade pip (offline?). Continuing with the bundled version."
}

Say "Installing career-fleet (this can take a minute the first time)..."
Push-Location $scriptDir
try {
  & $venvPy -m pip install . *>> $log
  $pipCode = $LASTEXITCODE
} finally {
  Pop-Location
}
if ($pipCode -ne 0) {
  Fail "Installing career-fleet failed. See $log for details. (Usually a network problem reaching PyPI.)"
}

& $venvPy -c "import career_fleet, harness_fleet" *>> $log
if ($LASTEXITCODE -ne 0) {
  Fail "career-fleet installed but could not be imported. See $log for details."
}

# Web discovery (search, job boards, article extraction) lives in an optional
# extra. It needs to compile a few packages, so treat it as best-effort.
Say "Adding web discovery (optional)..."
Push-Location $scriptDir
try {
  & $venvPy -m pip install ".[discover]" *>> $log
  $discoverCode = $LASTEXITCODE
} finally {
  Pop-Location
}
if ($discoverCode -eq 0) {
  Ok "Web discovery installed"
} else {
  Warn "Could not install web discovery (offline?). Sourcing will be limited."
}
Ok "career-fleet installed at $cli"

# --- 3. choose and create the workspace -------------------------------------

Say ""
Say "Step 3/8 - Creating the workspace"
$ws = if ($args.Count -ge 1) { $args[0] } else { Join-Path $HOME "career-fleet-workspace" }
New-Item -ItemType Directory -Force -Path $ws | Out-Null
# Canonicalize to an absolute path. Every step below Push-Location's into the
# workspace, so a relative --db would otherwise resolve against the *new*
# working directory and land in a nested path.
$ws = (Resolve-Path -LiteralPath $ws).Path
Ok "Workspace at $ws"

# Two files live here, by design: career_fleet.db holds your pipeline (companies,
# postings, lanes) and the shared engine keeps its own state in harness-fleet.db.
$careerDb = Join-Path $ws "career_fleet.db"
$engineDb = Join-Path $ws "harness-fleet.db"

# --- 4. install the bundled assistant skills --------------------------------

Say ""
Say "Step 4/8 - Installing the assistant skills"
Push-Location $ws
try {
  & $cli setup --workspace-root $ws *>> $log
  $setupCode = $LASTEXITCODE
} finally {
  Pop-Location
}
if ($setupCode -ne 0) { Fail "Could not install the assistant skills. See $log for details." }
Ok "Skills installed in $ws\.agents\skills"

# --- 5. create the database and your profile --------------------------------

Say ""
Say "Step 5/8 - Creating your profile"
Push-Location $ws
try {
  & $cli init --workspace-root $ws --db $careerDb *>> $log
  $initCode = $LASTEXITCODE
} finally {
  Pop-Location
}
if ($initCode -ne 0) { Fail "Could not initialize the workspace. See $log for details." }
Ok "Profile created at $ws\profile.json - edit it to say what you want"

# --- 6. run the offline demo -------------------------------------------------

Say ""
Say "Step 6/8 - Running the offline demo (no API keys needed)"
$demoJson = Join-Path $ws "runs\demo-01\clean_packet.json"
$demoCsv  = Join-Path $ws "runs\demo-01\clean_packet.csv"
if ((Test-Path $demoJson) -and (Test-Path $demoCsv)) {
  Ok "Demo already present - skipping"
} else {
  # The demo scores six sample postings with a built-in fake model, so it proves
  # the install without an account, a key, or any spend.
  Push-Location $ws
  try {
    & $venvPy -m harness_fleet.cli quickstart --demo --run-id demo-01 --db $engineDb *>> $log
    $demoCode = $LASTEXITCODE
  } finally {
    Pop-Location
  }
  if ($demoCode -ne 0) { Fail "The demo run failed. See $log for details." }
}
if (-not (Test-Path $demoCsv)) {
  Fail "The demo did not produce its expected output files. See $log for details."
}
Ok "Demo scored 6 sample postings"

# --- 7. register with Claude Desktop / Cursor --------------------------------

Say ""
Say "Step 7/8 - Registering with Claude Desktop / Cursor"
# Desktop apps do not inherit this shell's environment, so hand the caller's
# OpenRouter key to the MCP server when one is set (the CLI never prints values).
$mcpInstallArgs = @("-m", "harness_fleet.cli", "mcp", "install", "--workspace-root", $ws, "--db", $engineDb)
if ($env:OPENROUTER_API_KEY) {
  $mcpInstallArgs += @("--env", "OPENROUTER_API_KEY")
}
& $venvPy @mcpInstallArgs *>> $log
if ($LASTEXITCODE -eq 0) {
  Ok "MCP server registered"
  if ($env:OPENROUTER_API_KEY) {
    Say "  OPENROUTER_API_KEY passed into the client config (value not shown)"
  }
} else {
  Warn "Could not register with Claude Desktop / Cursor automatically."
  Say "  Add this entry to your MCP client config manually, then restart the client:"
  $manualEntry = @{
    mcpServers = @{
      "career-fleet" = @{
        command = $venvPy
        args    = @("-m", "harness_fleet.cli", "serve", "--workspace-root", $ws, "--db", $engineDb)
      }
    }
  } | ConvertTo-Json -Depth 5
  Say $manualEntry
}

# --- 8. getting free model access ------------------------------------------

Say ""
Say "Step 8/8 - Getting free model access (optional)"
Say "  Career Fleet's own lanes need no AI account at all - triage and recon run on rules"
Say "  and the text of the postings. This only matters for shared engine runs."
if ($env:OPENROUTER_API_KEY) {
  Ok "OpenRouter key detected - free OpenRouter models are ready for engine runs"
} elseif (Get-Command opencode -ErrorAction SilentlyContinue) {
  Ok "OpenCode is installed - sign in once if you have not: opencode auth login"
} else {
  Say "  Optional: connect a free model for engine runs. Pick one:"
  Say "  Pick whichever is easiest for you:"
  Say ""
  Say "  1. OpenRouter - one signup, works with every fleet:"
  Say "       https://openrouter.ai/          (create the account)"
  Say "       https://openrouter.ai/keys      (create a key, copy it)"
  Say "     then set it for your terminals:"
  Say "       setx OPENROUTER_API_KEY \"sk-or-...\""
  Say ""
  Say "  2. OpenCode - free hosted models:"
  Say "       npm install -g opencode-ai      (or: choco install opencode)"
  Say "       opencode auth login"
  Say ""
  Say "  3. Your own computer - no signup at all:  https://ollama.com/download"
  Say ""
  Say "  Full walkthrough, including what the free tiers actually allow:"
  Say "    $scriptDir\FREE-ACCESS.md"
}

# --- 8. summary --------------------------------------------------------------

Say ""
Say "--------------------------------------------------------------"
Say "  career-fleet is ready!"
Say "--------------------------------------------------------------"
Say ""
Say "  Installed:"
Say "    Python environment : $venvDir"
Say "    career-fleet CLI   : $cli"
Say "    Workspace          : $ws"
Say "    Your profile       : $ws\profile.json"
Say "    Demo results       : $demoCsv"
Say ""
Say "  Try next:"
Say "    1. Read the demo results (a spreadsheet):"
Say "         $demoCsv"
Say "       Those six sample postings were scored by the engine, so they live in"
Say "       $engineDb - your own pipeline below uses $careerDb."
Say "    2. Gather real companies:"
Say "         $cli discover --source yc --target W24 --max 30 --db `"$careerDb`""
Say "       then  $cli triage --db `"$careerDb`"   and   $cli recon --db `"$careerDb`""
Say "       and browse what you gathered:  $cli board --open --db `"$careerDb`""
Say "    3. Restart Claude Desktop (or Cursor) and just ask it to screen employers"
Say "       for you - the bundled career-fleet skill runs these steps for you."
Say ""
