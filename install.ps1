$ErrorActionPreference = 'Stop'
python -m venv "$PSScriptRoot/.venv"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& "$PSScriptRoot/.venv/Scripts/python.exe" -m pip install $PSScriptRoot
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
& "$PSScriptRoot/.venv/Scripts/career-fleet.exe" build
exit $LASTEXITCODE
