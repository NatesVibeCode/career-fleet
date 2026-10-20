"""Lifecycle adapter for the sovereign printed discovery Process."""
from __future__ import annotations

import errno
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


def _atomic_json(path: Path, value: dict) -> None:
    temporary = path.with_name(path.name + '.tmp-' + uuid.uuid4().hex)
    with open(temporary, 'x', opener=lambda p, flags: os.open(p, flags, 0o600)) as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def _snapshot() -> tuple[Path, Path, dict]:
    package, manifest = checked_manifest()
    worker = checked_worker()
    root = build_directory()
    retained = root / 'package'
    if not retained.exists():
        with tempfile.TemporaryDirectory(dir=root) as temporary:
            staged = Path(temporary) / 'package'
            shutil.copytree(package, staged)
            try:
                os.rename(staged, retained)
            except OSError as exc:
                if exc.errno not in (errno.EEXIST, errno.ENOTEMPTY) or not retained.is_dir():
                    raise
    return retained, worker, manifest


def execute(request_path: Path, run_directory: Path, browser_runtime: Path | None = None, evidence_source: Path | None = None, pinned_source: Path | None = None) -> tuple[dict, int]:
    raw = request_path.read_bytes()
    request_document = json.loads(raw)
    request = request_document.get('request', request_document)
    browser_selected = any(source.get('kind') == 'browser_html_search' for source in request.get('sources', []))
    if browser_selected and browser_runtime is None:
        raise ValueError('browser_html_search sources require --browser-runtime with pinned local runtime paths')
    paths = None
    if browser_runtime is not None:
        config = json.loads(browser_runtime.read_text())
        expected = {'node_executable', 'playwright_directory', 'browser_directory', 'browser_executable'}
        if not isinstance(config, dict) or set(config) != expected or any(not isinstance(config[key], str) or not config[key] for key in expected):
            raise ValueError('browser runtime JSON must contain exactly node_executable, playwright_directory, browser_directory, and browser_executable')
        paths = {key: str(Path(config[key]).expanduser().resolve(strict=True)) for key in expected}
        for key in ('node_executable', 'browser_executable'):
            if not Path(paths[key]).is_file() or not os.access(paths[key], os.X_OK):
                raise ValueError('browser runtime executable is unavailable')
        if not Path(paths['playwright_directory']).is_dir() or not Path(paths['browser_directory']).is_dir():
            raise ValueError('browser runtime directory is unavailable')
    if pinned_source is None:
        package, worker, manifest = _snapshot()
    else:
        original = checked_execution(pinned_source)
        package, worker = Path(original['package']), Path(original['worker'])
        manifest = json.loads((package / 'source-manifest.json').read_text())
    run_directory = run_directory.resolve()
    run_directory.mkdir(parents=True, mode=0o700, exist_ok=False)
    os.chmod(run_directory, 0o700)
    (run_directory / 'input.json').write_bytes(raw)
    os.chmod(run_directory / 'input.json', 0o600)
    (run_directory / 'journal').mkdir(mode=0o700)
    evidence = run_directory / 'evidence'
    if evidence_source is not None:
        shutil.copytree(evidence_source, evidence)
    else:
        evidence.mkdir(mode=0o700)
    bindings = run_directory / 'egress-bindings.json'
    compiled = json.loads((package / 'process.json').read_text())
    operations = {'discovery.captureretain.v1':'retain', 'discovery.captureload.v1':'load'}
    binding = {node['key']:{'operation':operations[node['node']['name']], 'directory':str(evidence)}
               for node in compiled['nodes'] if node['node']['name'] in operations}
    for item in compiled.get('process', {}).get('policy', {}).get('recovery', {}).get('bindings', []):
        block = item['reconciler']['key']
        name = item['reconciler']['block']['nodes'][0]['node']['name']
        if name in {'discovery.retainrecover.v1','discovery.loadrecover.v1'}:
            binding[f'{len(block)}:{block}observe'] = {'operation':'load','directory':str(evidence)}
    if paths is not None:
        (run_directory / 'browser-runtime').mkdir(mode=0o700)
        binding['15:acquire_browserbrowsercapture'] = {'operation': 'render', 'directory': str(run_directory / 'browser-runtime'), 'browser': paths}
    _atomic_json(bindings, binding)
    args = ['-egress-bindings', str(bindings)]
    key_path = run_directory / 'execution.key'
    with open(key_path, 'x', opener=lambda p, flags: os.open(p, flags, 0o600)) as stream:
        stream.write(secrets.token_hex(32))
    files = {'input.json': hashlib.sha256(raw).hexdigest(), 'execution.key': hashlib.sha256(key_path.read_bytes()).hexdigest()}
    files['egress-bindings.json'] = hashlib.sha256((run_directory / 'egress-bindings.json').read_bytes()).hexdigest()
    launch = {'schema': 'career_fleet.execution.v1', 'event_id': 'career-discovery-' + uuid.uuid4().hex,
              'process': manifest['process'], 'stage': manifest['stage'], 'package': str(package),
              'package_manifest_sha256': hashlib.sha256((package / 'source-manifest.json').read_bytes()).hexdigest(),
              'worker': str(worker), 'worker_sha256': hashlib.sha256(worker.read_bytes()).hexdigest(),
              'tool': manifest['tool'], 'worker_args': args, 'files': files, 'diagnostics': False}
    # Durable custody precedes launch, including when the parent is interrupted.
    _atomic_json(run_directory / 'execution.json', launch)
    return resume(run_directory)


def checked_execution(run_directory: Path) -> dict:
    run_directory = run_directory.resolve()
    launch = json.loads((run_directory / 'execution.json').read_text())
    if launch.get('schema') != 'career_fleet.execution.v1':
        raise ValueError('execution has no resumable launch manifest')
    for name, digest in launch['files'].items():
        if hashlib.sha256((run_directory / name).read_bytes()).hexdigest() != digest:
            raise ValueError(f'execution binding changed: {name}')
    worker, package = Path(launch['worker']), Path(launch['package'])
    if hashlib.sha256(worker.read_bytes()).hexdigest() != launch['worker_sha256']:
        raise ValueError('execution worker changed')
    if hashlib.sha256((package / 'source-manifest.json').read_bytes()).hexdigest() != launch['package_manifest_sha256']:
        raise ValueError('execution package manifest changed')
    manifest = json.loads((package / 'source-manifest.json').read_text())
    actual = {p.relative_to(package).as_posix() for p in package.rglob('*') if p.is_file()}
    if actual != set(manifest['files']) | {'source-manifest.json'}:
        raise ValueError('execution package inventory changed')
    for name, digest in manifest['files'].items():
        if hashlib.sha256((package / name).read_bytes()).hexdigest() != digest:
            raise ValueError(f'execution package changed: {name}')
    return launch


def resume(run_directory: Path) -> tuple[dict, int]:
    run_directory = run_directory.resolve()
    launch = checked_execution(run_directory)
    attempts = run_directory / 'attempts'
    attempts.mkdir(mode=0o700, exist_ok=True)
    attempt = attempts / uuid.uuid4().hex
    attempt.mkdir(mode=0o700)
    _atomic_json(attempt / 'started.json', {'event_id': launch['event_id']})
    env = dict(os.environ, CAREER_FLEET_EXECUTION_KEY=(run_directory / 'execution.key').read_text().strip())
    completed = subprocess.run([
        launch['worker'], '-cwd', launch['package'], '-process', 'process.json',
        '-tool', launch['tool'], '-input', str(run_directory / 'input.json'),
        '-event-id', launch['event_id'], '-event-state-dir', str(run_directory / 'journal'),
        '-deployment-key-env', 'CAREER_FLEET_EXECUTION_KEY', *(['-diagnostics'] if launch.get('diagnostics', True) else []), *launch['worker_args'],
    ], env=env, capture_output=True, text=True)
    for name, value in [('response.json', completed.stdout), ('stderr.txt', completed.stderr)]:
        (attempt / name).write_text(value)
        os.chmod(attempt / name, 0o600)
    _atomic_json(attempt / 'finished.json', {'exit_code': completed.returncode})
    try:
        response = json.loads(completed.stdout)
    except ValueError as exc:
        raise RuntimeError(f'worker produced no JSON response; inspect {attempt}') from exc
    _atomic_json(run_directory / 'response.json', response)
    (run_directory / 'stderr.txt').write_text(completed.stderr)
    return response, completed.returncode
