"""Lifecycle adapter for the sovereign printed discovery Process."""
from __future__ import annotations

import hashlib
import json
import os
import platform
from pathlib import Path
import secrets
import shutil
import subprocess
import tempfile
import uuid


def package_path() -> Path:
    return Path(__file__).resolve().parent / 'resources' / 'company_discovery'


def checked_manifest() -> tuple[Path, dict]:
    package = package_path()
    manifest = json.loads((package / 'source-manifest.json').read_text())
    for name, expected in manifest['files'].items():
        if hashlib.sha256((package / name).read_bytes()).hexdigest() != expected:
            raise ValueError(f'printed Process source changed: {name}')
    actual = {p.relative_to(package).as_posix() for p in package.rglob('*') if p.is_file()}
    if actual != set(manifest['files']) | {'source-manifest.json'}:
        raise ValueError('printed Process has unexpected or missing files')
    return package, manifest


def build_directory() -> Path:
    package, _ = checked_manifest()
    digest = hashlib.sha256((package / 'source-manifest.json').read_bytes()).hexdigest()
    root = Path(os.environ.get('CAREER_FLEET_CACHE', Path.home() / '.cache' / 'career-fleet'))
    return root / digest / (platform.system().lower() + "-" + platform.machine().lower())


def build_worker() -> Path:
    package, _ = checked_manifest()
    root = build_directory()
    root.mkdir(parents=True, exist_ok=True)
    worker = root / ('worker.exe' if os.name == 'nt' else 'worker')
    if worker.exists():
        return checked_worker()
    go = shutil.which('go')
    if not go:
        raise RuntimeError('Go 1.26+ is required for the explicit build step')
    env = dict(os.environ, GOWORK='off', GOPROXY='off', GOSUMDB='off', GOTOOLCHAIN='local', CGO_ENABLED='0')
    with tempfile.TemporaryDirectory(dir=root) as temporary:
        built = Path(temporary) / worker.name
        subprocess.run([go, 'build', '-trimpath', '-o', str(built), './worker'], cwd=package, env=env, check=True)
        digest = hashlib.sha256(built.read_bytes()).hexdigest()
        os.replace(built, worker)
        (root / 'build.json').write_text(json.dumps({'sha256': digest, 'source_manifest_sha256': hashlib.sha256((package / 'source-manifest.json').read_bytes()).hexdigest()}, indent=2) + '\n')
    return worker


def checked_worker() -> Path:
    root = build_directory()
    worker = root / ('worker.exe' if os.name == 'nt' else 'worker')
    if not worker.is_file() or not (root / 'build.json').is_file():
        raise RuntimeError("Build the bundled Process first: career-fleet build")
    expected = json.loads((root / 'build.json').read_text())['sha256']
    if hashlib.sha256(worker.read_bytes()).hexdigest() != expected:
        raise ValueError('built discovery worker changed')
    return worker


def execute(request_path: Path, run_directory: Path) -> tuple[dict, int]:
    package, manifest = checked_manifest()
    worker = checked_worker()
    raw = request_path.read_bytes()
    # Input semantics, configuration and validation remain inside the Process.
    json.loads(raw)
    run_directory.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(run_directory, 0o700)
    (run_directory / 'input.json').write_bytes(raw)
    journal = run_directory / 'journal'
    journal.mkdir(mode=0o700)
    event_id = 'career-discovery-' + uuid.uuid4().hex
    key = secrets.token_hex(32)
    key_path = run_directory / 'execution.key'
    with open(key_path, 'x', opener=lambda p, flags: os.open(p, flags, 0o600)) as stream:
        stream.write(key)
    env = dict(os.environ, CAREER_FLEET_EXECUTION_KEY=key)
    completed = subprocess.run([
        str(worker), '-cwd', str(package), '-process', 'process.json',
        '-tool', manifest['tool'], '-input', str((run_directory / 'input.json').resolve()),
        '-event-id', event_id, '-event-state-dir', str(journal.resolve()),
        '-deployment-key-env', 'CAREER_FLEET_EXECUTION_KEY', '-diagnostics',
    ], env=env, capture_output=True, text=True)
    (run_directory / 'response.json').write_text(completed.stdout)
    (run_directory / 'stderr.txt').write_text(completed.stderr)
    (run_directory / 'execution.json').write_text(json.dumps({
        'event_id': event_id, 'request_sha256': hashlib.sha256(raw).hexdigest(),
        'process': manifest['process'], 'stage': manifest['stage'], 'exit_code': completed.returncode,
    }, indent=2) + '\n')
    try:
        response = json.loads(completed.stdout)
    except ValueError as exc:
        raise RuntimeError(f'worker produced no JSON response; inspect {run_directory}') from exc
    return response, completed.returncode
