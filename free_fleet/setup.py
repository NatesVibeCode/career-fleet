"""Fresh-system setup for skill discovery and local SQLite state."""
from __future__ import annotations

import filecmp
import os
import shutil
import sys
from pathlib import Path
from typing import Literal

from .catalog import RouteCatalog
from .models import SetupAction, SetupReport, StdioServerConfig
from .store import BulkLanesStore, FreeFleetStore


ScopeName = Literal["user", "project"]


def bundled_skill_path() -> Path:
    path = Path(__file__).resolve().parent / "resources" / "skill"
    if not (path / "SKILL.md").is_file():
        raise RuntimeError("installed package is missing the bundled free-fleet skill")
    return path


def skill_destination(
    scope: ScopeName,
    home: Path,
    workspace: Path,
    skill_root: str | Path | None = None,
) -> Path:
    if skill_root is None:
        root = (home if scope == "user" else workspace) / ".agents" / "skills"
    else:
        candidate = Path(skill_root).expanduser()
        root = (candidate if candidate.is_absolute() else workspace / candidate).resolve()
    return root / "free-fleet"


def _relative_files(root: Path) -> set[Path]:
    return {path.relative_to(root) for path in root.rglob("*") if path.is_file()}


def _same_skill(source: Path, destination: Path) -> bool:
    source_files = _relative_files(source)
    destination_files = _relative_files(destination) if destination.is_dir() else set()
    return source_files == destination_files and all(
        filecmp.cmp(source / relative, destination / relative, shallow=False) for relative in source_files
    )


def installed_skill_matches(destination: Path) -> bool:
    return destination.is_dir() and _same_skill(bundled_skill_path(), destination)


def _install_skill(source: Path, destination: Path, *, dry_run: bool, force: bool) -> SetupAction:
    if destination.exists() and not destination.is_dir():
        raise ValueError(f"skill destination is not a directory: {destination}")
    if destination.is_dir() and _same_skill(source, destination):
        return SetupAction(kind="skill", status="unchanged", path=str(destination))
    if destination.exists() and not force:
        raise FileExistsError(
            f"a different skill already exists at {destination}; rerun with --force to update managed files"
        )
    status = "planned" if dry_run else ("updated" if destination.exists() else "created")
    if not dry_run:
        destination.mkdir(parents=True, exist_ok=True)
        for source_file in source.rglob("*"):
            if source_file.is_file():
                relative = source_file.relative_to(source)
                target = destination / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(source_file, target)
    return SetupAction(kind="skill", status=status, path=str(destination))


def installed_cli_path() -> str:
    for directory in (Path(sys.executable).absolute().parent, Path(sys.prefix) / "Scripts"):
        for name in ("account-fleet", "free-fleet", "bulk-lanes"):
            for suffix in (".exe", "") if sys.platform == "win32" else ("",):
                sibling = directory / (name + suffix)
                if sibling.is_file():
                    return str(sibling)
    for name in ("account-fleet", "free-fleet", "bulk-lanes"):
        discovered = shutil.which(name)
        if discovered:
            return str(Path(discovered).resolve())
    return "free-fleet"


def setup_workspace(
    *,
    scope: ScopeName,
    workspace_root: str | Path,
    db_path: str | Path | None = None,
    skill_root: str | Path | None = None,
    home: str | Path | None = None,
    dry_run: bool = False,
    force: bool = False,
    refresh_routes: bool = False,
) -> SetupReport:
    workspace = Path(workspace_root).expanduser().resolve(strict=True)
    if not workspace.is_dir():
        raise ValueError("workspace root must be a directory")
    home_path = Path(home).expanduser().resolve() if home is not None else Path.home().resolve()
    destination = skill_destination(scope, home_path, workspace, skill_root)
    database = Path(db_path).expanduser() if db_path is not None else Path("free-fleet.db")
    database = (database if database.is_absolute() else workspace / database).resolve()
    if not database.is_relative_to(workspace):
        raise ValueError("setup database must stay below the workspace root")

    actions = [_install_skill(bundled_skill_path(), destination, dry_run=dry_run, force=force)]
    account_source = Path(__file__).resolve().parent / "resources" / "account_skill"
    if account_source.is_dir():
        actions.append(_install_skill(account_source, destination.parent / "account-fleet", dry_run=dry_run, force=force))
    refresh_result: dict[str, int | str] | None = None
    if dry_run:
        actions.append(SetupAction(kind="database", status="planned", path=str(database)))
        actions.append(SetupAction(
            kind="routes",
            status="planned" if refresh_routes else "skipped",
            detail="provider discovery" if refresh_routes else "run routes --refresh when ready",
        ))
        observed_route_count = 0
    else:
        existed = database.exists()
        store = FreeFleetStore(database)
        actions.append(SetupAction(
            kind="database",
            status="unchanged" if existed else "created",
            path=str(database),
            detail=f"SQLite schema {store.schema_version()}",
        ))
        catalog = RouteCatalog(db_path=database)
        if refresh_routes:
            refresh_result = catalog.refresh_all()
            actions.append(SetupAction(kind="routes", status="updated", detail="provider catalogues refreshed"))
        else:
            actions.append(SetupAction(kind="routes", status="skipped", detail="run routes --refresh when ready"))
        observed_route_count = len(catalog.get_routes(free_only=True))

    from .providers.registry import configured_routes
    provider_ready = not dry_run and bool(configured_routes(catalog.get_routes(free_only=True)))
    skill_ready = dry_run or actions[0].status in {"created", "updated", "unchanged"}
    ready = not dry_run and skill_ready and provider_ready and observed_route_count > 0
    cli_command = installed_cli_path()
    stdio = StdioServerConfig(
        command=cli_command,
        args=["serve", "--workspace-root", str(workspace), "--db", str(database)],
    )
    next_commands: list[list[str]] = []
    if not refresh_routes or observed_route_count == 0:
        next_commands.append(["free-fleet", "routes", "--db", str(database), "--refresh", "--json"])
    next_commands.extend([
        ["free-fleet", "doctor", "--db", str(database), "--workspace-root", str(workspace), "--json"],
        ["free-fleet", "init", "my-task", "--db", str(database), "--preset", "classify"],
    ])
    return SetupReport(
        ready=ready,
        scope=scope,
        workspace_root=str(workspace),
        database=str(database),
        skill_path=str(destination),
        actions=actions,
        stdio_server=stdio,
        route_refresh=refresh_result,
        next_commands=next_commands,
    )
