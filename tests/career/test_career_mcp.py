"""Headless MCP consumption uses the actual Career Fleet printed Process."""
import asyncio
import json
from pathlib import Path
import sys
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client
from career_fleet.process_runtime import build_worker


def test_headless_mcp_discovery_and_continuation(tmp_path):
    build_worker()
    repository = Path(__file__).resolve().parents[2]
    request = tmp_path / 'request.json'
    request.write_bytes((repository / 'examples/company-discovery/anonymous-sparse.json').read_bytes())
    async def consume():
        params = StdioServerParameters(command=sys.executable, args=['-m', 'career_fleet.cli', 'serve', '--workspace-root', str(tmp_path)], cwd=str(repository))
        async with stdio_client(params) as (read, write):
            async with ClientSession(read, write) as session:
                await session.initialize()
                tools = await session.list_tools()
                names = {tool.name for tool in tools.tools}
                assert {'career_fleet_init', 'career_fleet_campaign_run', 'career_fleet_sources_inspect', 'career_fleet_blocks_list', 'career_fleet_blocks_verify', 'career_fleet_campaign_resume'} <= names
                initialized = await session.call_tool('career_fleet_init', {'objective':'discover businesses', 'output_path':'selected.json', 'families':['exhibitors']})
                assert not initialized.isError
                assert len(json.loads((tmp_path / 'selected.json').read_text())['request']['source_blocks']) == 2
                result = await session.call_tool('career_fleet_campaign_run', {'request_path': 'request.json', 'directory': 'campaign'})
                assert not result.isError
                value = result.structuredContent
                assert value['ok'] and value['result']['inference_calls'] == 0
                resumed = await session.call_tool('career_fleet_campaign_resume', {'directory': 'campaign'})
                assert not resumed.isError and resumed.structuredContent['event_id'] == value['event_id']
                next_request = json.loads(request.read_text())
                carried = next_request.get('request', next_request)
                carried.update(seeds=[], prior=value['result']['candidates'], questions=[{'id':'new_metric', 'field':'operator_metric', 'scope':'', 'acquisition':'', 'query_template':'', 'source_id':'', 'link_terms':[], 'rules':[]}])
                (tmp_path / 'next-question.json').write_text(json.dumps(next_request))
                again = await session.call_tool('career_fleet_discover', {'request_path':'next-question.json', 'run_directory':'next-question'})
                assert not again.isError and again.structuredContent['ok']
                assert {c['id'] for c in again.structuredContent['result']['candidates']} == {c['id'] for c in value['result']['candidates']}
                escaped = await session.call_tool('career_fleet_discover', {'request_path': '../outside.json'})
                assert escaped.isError
    asyncio.run(consume())


def test_mcp_optional_properties_preserve_absence_and_reject_explicit_null():
    import pytest
    from pydantic import ValidationError
    from career_fleet.mcp_server import _schema_model
    schema = {'type':'object', 'properties':{'name':{'type':'string'}, 'detail':{'type':'object', 'properties':{'website':{'type':'string'}}, 'required':[], 'additionalProperties':False}}, 'required':['name','detail'], 'additionalProperties':False}
    Result = _schema_model(schema, 'OptionalRoundTrip')
    native = {'name':'Listed business', 'detail':{}}
    assert Result.model_validate(native).model_dump(mode='json') == native
    assert Result.model_validate_json(Result.model_validate(native).model_dump_json()).model_dump(mode='json') == native
    with pytest.raises(ValidationError):
        Result.model_validate({'name':'Listed business', 'detail':{'website':None}})
