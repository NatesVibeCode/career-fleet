from pathlib import Path

import pytest

from harness_fleet.setup import bundled_skill_path, setup_workspace
from harness_fleet.store import HarnessStore


def test_setup_installs_bundled_skill_and_database_idempotently(tmp_path):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    home.mkdir()
    workspace.mkdir()

    first = setup_workspace(
        scope="user",
        workspace_root=workspace,
        home=home,
    )
    second = setup_workspace(
        scope="user",
        workspace_root=workspace,
        home=home,
    )

    assert first.actions[0].status == "created"
    assert second.actions[0].status == "unchanged"
    assert Path(first.skill_path, "SKILL.md").is_file()
    assert HarnessStore(first.database).schema_version() == "5"
    # installed_cli_path() returns the first product CLI found in this
    # environment (or the "harness-fleet" fallback when none is installed),
    # so any product name the resolver searches is a correct answer here.
    assert Path(first.stdio_server.command).stem in {
        "harness-fleet", "account-fleet", "career-fleet", "career-lanes",
    }
    assert Path(first.skill_path).with_name("account-fleet").joinpath("SKILL.md").is_file()
    assert first.database in first.stdio_server.args
    assert first.ready is False  # packaged route hints are not fresh price evidence


def test_user_scope_uses_portable_agents_directory(tmp_path):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    home.mkdir()
    workspace.mkdir()
    report = setup_workspace(
        scope="user",
        workspace_root=workspace,
        home=home,
        dry_run=True,
    )
    assert report.skill_path == str(home / ".agents/skills/harness-fleet")


def test_custom_skill_root_supports_nonstandard_harness(tmp_path):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    home.mkdir()
    workspace.mkdir()
    report = setup_workspace(
        scope="project",
        workspace_root=workspace,
        skill_root=".any-harness/skills",
        home=home,
        dry_run=True,
    )
    assert report.skill_path == str(workspace / ".any-harness/skills/harness-fleet")


def test_stdio_server_uses_current_python_environment(tmp_path, monkeypatch):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    environment = tmp_path / "tool-environment/bin"
    home.mkdir()
    workspace.mkdir()
    environment.mkdir(parents=True)
    executable = environment / "harness-fleet"
    executable.write_text("#!/bin/sh\n")
    monkeypatch.setattr("harness_fleet.setup.sys.executable", str(environment / "python"))

    report = setup_workspace(
        scope="project",
        workspace_root=workspace,
        home=home,
        dry_run=True,
    )

    assert report.stdio_server.command == str(executable)


def test_setup_refuses_different_existing_skill_without_force(tmp_path):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    destination = home / ".agents/skills/harness-fleet"
    destination.mkdir(parents=True)
    (destination / "SKILL.md").write_text("different")
    workspace.mkdir()

    with pytest.raises(FileExistsError, match="--force"):
        setup_workspace(
            scope="user",
            workspace_root=workspace,
            home=home,
        )


def test_force_update_removes_stale_managed_skill_files(tmp_path):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    home.mkdir()
    workspace.mkdir()
    setup_workspace(scope="user", workspace_root=workspace, home=home)
    stale = home / ".agents/skills/harness-fleet/stale-from-old-release.md"
    stale.write_text("stale")

    setup_workspace(scope="user", workspace_root=workspace, home=home, force=True)

    assert not stale.exists()


def test_setup_preflights_all_skill_destinations_before_writing(tmp_path):
    home = tmp_path / "home"
    workspace = tmp_path / "workspace"
    conflict = home / ".agents/skills/account-fleet"
    home.mkdir()
    workspace.mkdir()
    conflict.mkdir(parents=True)
    (conflict / "SKILL.md").write_text("user skill")

    with pytest.raises(FileExistsError, match="--force"):
        setup_workspace(scope="user", workspace_root=workspace, home=home)

    assert not (home / ".agents/skills/harness-fleet").exists()
    assert not (workspace / "harness-fleet.db").exists()


def test_setup_database_cannot_escape_workspace(tmp_path):
    workspace = tmp_path / "workspace"
    workspace.mkdir()
    with pytest.raises(ValueError, match="workspace root"):
        setup_workspace(
            scope="project",
            workspace_root=workspace,
            db_path=tmp_path / "outside.db",
            home=tmp_path,
            dry_run=True,
        )


def test_packaged_skill_is_complete():
    root = bundled_skill_path()
    assert {
        Path("SKILL.md"),
        Path("references/operations.md"),
        Path("references/task-contracts.md"),
        Path("agents/openai.yaml"),
    } <= {path.relative_to(root) for path in root.rglob("*") if path.is_file()}


def test_all_distributed_skill_copies_match():
    packaged = bundled_skill_path()
    repository = Path(__file__).resolve().parents[1]
    expected = {path.relative_to(packaged): path.read_bytes() for path in packaged.rglob("*") if path.is_file()}
    for root in [repository / "skills/harness-fleet", repository / ".agents/skills/harness-fleet"]:
        actual = {path.relative_to(root): path.read_bytes() for path in root.rglob("*") if path.is_file()}
        assert actual == expected


def test_account_skill_and_examples_are_bundled():
    repository = Path(__file__).resolve().parents[1]
    resources = repository / "harness_fleet/resources"
    expected = {p.relative_to(resources / "account_skill"): p.read_bytes() for p in (resources / "account_skill").rglob("*") if p.is_file()}
    assert expected
    for root in [repository / "skills/account-fleet", repository / ".agents/skills/account-fleet"]:
        assert {p.relative_to(root): p.read_bytes() for p in root.rglob("*") if p.is_file()} == expected
    for name in ("task.json", "sample_accounts.csv"):
        assert (resources / "examples/account_research" / name).read_bytes() == (repository / "examples/account_research" / name).read_bytes()
