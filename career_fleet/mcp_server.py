"""Career Fleet MCP: typed lifecycle access to the same standalone Process."""
import hashlib
import json
from pathlib import Path
from typing import Any
import uuid
from mcp.server.fastmcp import FastMCP
from pydantic import BaseModel, ConfigDict, create_model, model_serializer
from career_fleet import operations
from career_fleet.process_runtime import package_path
from career_fleet.research_store import ResearchStore


class _TypedOutput(BaseModel):
    model_config = ConfigDict(extra='forbid')

    @model_serializer(mode='wrap')
    def _omit_absent(self, handler):
        # Native optional properties allow absence, never a fabricated JSON null.
        return {key: value for key, value in handler(self).items() if value is not None}


def _schema_model(schema: dict, name: str, cache: dict | None = None) -> Any:
    cache = {} if cache is None else cache
    digest = hashlib.sha256(json.dumps(schema, sort_keys=True).encode()).hexdigest()
    if digest in cache:
        return cache[digest]
    kind = schema['type']
    if kind == 'array':
        return list[_schema_model(schema['items'], name + 'Item', cache)]
    if kind != 'object':
        return {'string': str, 'integer': int, 'boolean': bool}[kind]
    required = set(schema.get('required', []))
    fields = {}
    for key, child in schema.get('properties', {}).items():
        annotation = _schema_model(child, name + key.title().replace('_', ''), cache)
        fields[key] = (annotation, ...) if key in required else (annotation, None)
    model = create_model(name, __base__=_TypedOutput, **fields)
    cache[digest] = model
    return model


def create_mcp_server(workspace_root: str | Path, db_path: str | Path = 'career_fleet.db') -> FastMCP:
    root = Path(workspace_root).expanduser().resolve(strict=True)
    if not root.is_dir():
        raise ValueError('workspace root must be a directory')

    def path(value: str | Path, exists: bool = False) -> Path:
        raw = Path(value).expanduser()
        resolved = (root / raw).resolve() if not raw.is_absolute() else raw.resolve()
        if not resolved.is_relative_to(root):
            raise ValueError('path escapes Career Fleet workspace')
        if exists and not resolved.exists():
            raise ValueError(f'missing workspace path: {value}')
        return resolved

    db = path(db_path)
    # The output contract comes from the actual printed Node, not a second model.
    spec = json.loads((package_path() / 'nodes/done/done/spec.json').read_text())
    result_schema = next(field['type'] for field in spec['fields'] if field['side'] == 'output' and field['name'] == 'result')
    Result = _schema_model(result_schema, 'DiscoveryResult')
    Response = create_model('CareerFleetResponse', __config__=ConfigDict(extra='forbid'), ok=(bool, ...), event_id=(str, ''), result=(Result | None, None), error=(str, ''))
    server = FastMCP('career-fleet', instructions='Generic company-first discovery through the pinned standalone Process. Criteria and acquisition authority come from explicit requests.')

    def output(response: dict) -> BaseModel:
        return Response(ok=response.get('ok', False), event_id=response.get('event_id', ''), result=response.get('outputs', {}).get('result'), error=response.get('error', ''))

    @server.tool(structured_output=True)
    def career_fleet_init(objective: str, output_path: str, source_ids: list[str] | None = None,
                          families: list[str] | None = None, pages: int = 1,
                          enable_acquisition: bool = False, enable_browser: bool = False,
                          max_requests: int | None = None) -> dict[str, str]:
        """Create a generic request using selected channels or verified source Blocks."""
        target = operations.initialize(objective, path(output_path), pages=pages, enable_browser=enable_browser,
                                      enable_acquisition=enable_acquisition, max_requests=max_requests,
                                      source_ids=source_ids, families=families)
        return {'path': str(target)}

    @server.tool(structured_output=True)
    def career_fleet_discover(request_path: str, run_directory: str | None = None, browser_runtime: str | None = None) -> Response:
        """Run an explicit research request and retain unresolved results."""
        response, _ = operations.discover(path(request_path, True), db, path(run_directory or 'runs/' + uuid.uuid4().hex), path(browser_runtime, True) if browser_runtime else None)
        return output(response)

    @server.tool(structured_output=True)
    def career_fleet_sources_list() -> list[dict[str, Any]]:
        """List configured sources and evidence-verified Block revisions."""
        return operations.sources(db)

    @server.tool(structured_output=True)
    def career_fleet_sources_discover(request_path: str, run_directory: str | None = None, browser_runtime: str | None = None) -> Response:
        """Find collection sources and inspect known structures through the Process."""
        response, _ = operations.discover_sources(path(request_path, True), db, path(run_directory or 'runs/' + uuid.uuid4().hex), path(browser_runtime, True) if browser_runtime else None)
        return output(response)

    @server.tool(structured_output=True)
    def career_fleet_sources_inspect(candidate_path: str, preset: str = 'auto_collection', run_directory: str | None = None) -> Response:
        """Verify one retained collection candidate using a data-only preset."""
        response, _ = operations.inspect_source(candidate_path if candidate_path.startswith('source-candidate:') else path(candidate_path, True), preset, db, path(run_directory or 'runs/' + uuid.uuid4().hex))
        return output(response)

    @server.tool(structured_output=True)
    def career_fleet_blocks_list() -> list[dict[str, Any]]:
        """List exact Forge-published source Block references and real-use evidence."""
        return operations.blocks(db)

    @server.tool(structured_output=True)
    def career_fleet_blocks_verify(block_id: str, run_directory: str | None = None) -> Response:
        """Execute an immutable source Block through the printed discovery Process."""
        response, _ = operations.verify_block(block_id, db, path(run_directory or 'runs/' + uuid.uuid4().hex))
        return output(response)

    @server.tool(structured_output=True)
    def career_fleet_campaign_run(request_path: str, directory: str, browser_runtime: str | None = None) -> Response:
        """Start a persisted campaign; Process owns its actions and recovery."""
        response, _ = operations.campaign_operation('run', path(directory), db, path(request_path, True), path(browser_runtime, True) if browser_runtime else None)
        return output(response)

    @server.tool(structured_output=True)
    def career_fleet_campaign_status(directory: str) -> dict[str, Any]:
        """Read retained Process progress and execution lineage."""
        return operations.campaign_operation('status', path(directory, True), db)[0]

    @server.tool(structured_output=True)
    def career_fleet_campaign_resume(directory: str) -> Response:
        """Continue the same exact event through its native journal."""
        return output(operations.campaign_operation('resume', path(directory, True), db)[0])

    @server.tool(structured_output=True)
    def career_fleet_campaign_replay(directory: str) -> Response:
        """Create a labeled execution from retained evidence without network reads."""
        return output(operations.campaign_operation('replay', path(directory, True), db)[0])

    @server.tool(structured_output=True)
    def career_fleet_campaign_refresh(directory: str) -> Response:
        """Create a new event with fresh acquisition under the original request policy."""
        return output(operations.campaign_operation('refresh', path(directory, True), db)[0])

    @server.tool(structured_output=True)
    def career_fleet_list(event_id: str | None = None) -> list[dict[str, Any]]:
        """List retained leads, including unresolved organizations."""
        store = ResearchStore(db)
        try:
            return store.leads(event_id)
        finally:
            store.close()

    @server.tool(structured_output=True)
    def career_fleet_dossier(lead_id: str) -> list[dict[str, Any]]:
        """Read every retained version of one exact lead."""
        store = ResearchStore(db)
        try:
            return store.dossier(lead_id)
        finally:
            store.close()

    @server.tool(structured_output=True)
    def career_fleet_export(output_path: str, event_id: str | None = None) -> dict[str, str]:
        """Export exact retained data to a new workspace file."""
        target = path(output_path)
        store = ResearchStore(db)
        try:
            value = store.run(event_id) if event_id else store.leads()
            with target.open('x', encoding='utf-8') as stream:
                json.dump(value, stream, ensure_ascii=False, indent=2)
            return {'path': str(target)}
        finally:
            store.close()

    return server
