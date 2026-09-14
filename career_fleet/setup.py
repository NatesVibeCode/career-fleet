"""Install the bundled Career Fleet assistant skill into a workspace."""
from __future__ import annotations

import shutil
from pathlib import Path
from typing import Any


def bundled_skill_path() -> Path:
    path = Path(__file__).resolve().parent / "resources" / "skill" / "career-fleet"
    if not path.is_dir():
        raise RuntimeError("installed package is missing the bundled career-fleet skill")
    return path


def _same_tree(source: Path, destination: Path) -> bool:
    if not destination.is_dir():
        return False
    source_files = sorted(p.relative_to(source) for p in source.rglob("*") if p.is_file())
    destination_files = sorted(p.relative_to(destination) for p in destination.rglob("*") if p.is_file())
    if source_files != destination_files:
        return False
    return all(
        (source / relative).read_bytes() == (destination / relative).read_bytes()
        for relative in source_files
    )


def install_skill(workspace_root: Path | str, *, force: bool = False) -> dict[str, Any]:
    """Install the bundled skill under ``<workspace>/.agents/skills``."""
    workspace = Path(workspace_root).expanduser().resolve()
    if not workspace.is_dir():
        raise ValueError(f"workspace root must be a directory: {workspace}")

    source = bundled_skill_path()
    destination = workspace / ".agents" / "skills" / "career-fleet"
    if _same_tree(source, destination):
        return {"status": "success", "action": "unchanged", "skill_path": str(destination)}
    if destination.exists() or destination.is_symlink():
        if not force:
            raise ValueError(
                f"a different Career Fleet skill already exists at {destination}; rerun with --force to replace it"
            )
        if destination.is_dir() and not destination.is_symlink():
            shutil.rmtree(destination)
        else:
            destination.unlink()

    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(source, destination)
    return {"status": "success", "action": "created", "skill_path": str(destination)}
