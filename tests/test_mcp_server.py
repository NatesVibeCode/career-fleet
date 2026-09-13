import json
import subprocess
import sys

import pytest

from free_fleet.mcp_server import Workspace, create_mcp_server


def test_workspace_rejects_escape(tmp_path):
    workspace = Workspace(tmp_path)
    with pytest.raises(ValueError, match="escapes workspace"):
        workspace.path("../outside.json")


def test_mcp_honors_bulk_lanes_database_environment(tmp_path, monkeypatch):
    configured = tmp_path / "configured.db"
    monkeypatch.setenv("BULK_LANES_DB", str(configured))

    create_mcp_server(tmp_path)

    assert configured.exists()
    assert not (tmp_path / "free-fleet.db").exists()


def test_mcp_tool_error_does_not_kill_server(tmp_path):
    messages = [
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "pytest", "version": "1"},
            },
        },
        {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}},
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_test",
                "arguments": {"task": "missing", "input_path": "missing.jsonl"},
            },
        },
        {"jsonrpc": "2.0", "id": 3, "method": "tools/list", "params": {}},
    ]
    process = subprocess.Popen(
        [sys.executable, "-m", "free_fleet.cli", "serve", "--workspace-root", str(tmp_path)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    assert process.stdin is not None
    assert process.stdout is not None
    responses = []
    for message in messages:
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()
        if "id" in message:
            responses.append(json.loads(process.stdout.readline()))
    process.stdin.close()
    exit_code = process.wait(timeout=10)

    assert exit_code == 0
    assert responses[1]["id"] == 2
    assert responses[1]["result"]["isError"] is True
    assert responses[2]["id"] == 3
    tools = responses[2]["result"]["tools"]
    assert len(tools) == 16
    tool_names = {tool["name"] for tool in tools}
    assert "free_fleet_init" in tool_names
    assert "free_fleet_save_profile" in tool_names
    assert "free_fleet_get_profile" in tool_names
    assert "free_fleet_status" in tool_names
    assert "free_fleet_eval" in tool_names
    assert "free_fleet_cooldowns" in tool_names
    assert all(tool["description"] for tool in tools)
    assert all("inputSchema" in tool and "outputSchema" in tool for tool in tools)


def test_mcp_tools_execution(tmp_path):
    messages = [
        {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "initialize",
            "params": {
                "protocolVersion": "2024-11-05",
                "capabilities": {},
                "clientInfo": {"name": "pytest", "version": "1"},
            },
        },
        {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}},
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_doctor",
                "arguments": {},
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_schema",
                "arguments": {"kind": "database"},
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 4,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_routes",
                "arguments": {"refresh": False, "observed_zero_only": False},
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 5,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_init",
                "arguments": {"task_name": "mcp-score-task", "preset": "score"},
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 6,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_save_profile",
                "arguments": {"profile": {"profile_name": "MCP ICP", "required_stack": ["PostgreSQL"]}},
            },
        },
        {
            "jsonrpc": "2.0",
            "id": 7,
            "method": "tools/call",
            "params": {
                "name": "free_fleet_get_profile",
                "arguments": {},
            },
        },
    ]
    process = subprocess.Popen(
        [sys.executable, "-m", "free_fleet.cli", "serve", "--workspace-root", str(tmp_path)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
    )
    assert process.stdin is not None
    assert process.stdout is not None
    responses = {}
    for message in messages:
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()
        if "id" in message:
            resp = json.loads(process.stdout.readline())
            responses[resp["id"]] = resp
    process.stdin.close()
    exit_code = process.wait(timeout=10)

    assert exit_code == 0
    # doctor tool call
    doctor_res = responses[2]["result"]
    assert "isError" not in doctor_res or not doctor_res["isError"]

    # schema tool call
    schema_res = responses[3]["result"]
    assert "isError" not in schema_res or not schema_res["isError"]

    # routes tool call
    routes_res = responses[4]["result"]
    assert "isError" not in routes_res or not routes_res["isError"]

    # init tool call
    init_res = responses[5]["result"]
    assert "isError" not in init_res or not init_res["isError"]
    assert "structuredContent" in init_res
    assert init_res["structuredContent"]["task"] == "mcp-score-task"

    profile_res = responses[6]["result"]
    assert profile_res["structuredContent"]["profile_kind"] == "ideal_company"
    assert responses[7]["result"]["structuredContent"]["revision"] == profile_res["structuredContent"]["revision"]
