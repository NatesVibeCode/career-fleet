"""The new discovery boundary has an explicit source plan and sealed source inventory."""
import json
from pathlib import Path
import shutil
import pytest
from career_fleet.cli import main
from career_fleet.channel_catalog import default_sources
import career_fleet.process_runtime as runtime


def test_initial_request_has_generic_multi_channel_sources(tmp_path):
    path = tmp_path / 'request.json'
    assert main(['init', '--objective', 'water filtration', '--output', str(path)]) == 0
    request = json.loads(path.read_text())['request']
    sources = request['sources']
    assert len(sources) == 40
    assert len(request['source_blocks']) == 12
    assert {source['kind'] for source in sources} >= {'html_search', 'json_mapped', 'rss'}
    ids = {source['id'] for source in sources}
    assert {
        'web.duckduckgo', 'news.google', 'github.repositories',
        'stackexchange.stackoverflow', 'reddit.search', 'directory.ycombinator',
        'directory.ycombinator.api', 'discourse.meta.api',
        'discourse.cloudflare.api', 'discourse.python.api',
    } <= ids
    yc_api = next(source for source in sources if source['id'] == 'directory.ycombinator.api')
    assert yc_api['kind'] == 'json_mapped'
    assert yc_api['mapping']['items_path'] == '/companies'
    assert '{page}' in yc_api['url_template']
    assert any('{objective}' in query for source in sources for query in source['queries'])
    assert request['criteria'] == []
    assert request['questions'] == []
    assert request['policy']['acquisition_enabled'] is False
    assert request['reusable_captures'] == []


def test_browser_catalog_is_explicit_and_selects_dynamic_marketplaces(tmp_path):
    path = tmp_path / 'browser-request.json'
    assert main(['init', '--objective', 'water filtration', '--enable-browser', '--output', str(path)]) == 0
    sources = json.loads(path.read_text())['request']['sources']
    kinds = {source['id']: source['kind'] for source in sources}
    for source_id in {
        'marketplace.producthunt', 'marketplace.wellfound', 'marketplace.g2',
        'directory.capterra', 'marketplace.shopify_apps', 'marketplace.salesforce',
    }:
        assert kinds[source_id] == 'browser_html_search'
    producthunt = next(source for source in sources if source['id'] == 'marketplace.producthunt')
    assert producthunt['browser_mapping']['item_selector'] == 'button[data-test^="spotlight-result-product-"]'
    assert producthunt['browser_mapping']['key_attribute'] == 'data-test'
    wellfound = next(source for source in sources if source['id'] == 'marketplace.wellfound')
    assert wellfound['browser_mapping']['url_selector'] == 'a[href^="/company/"]'
    assert '{page}' in wellfound['url_template']
    assert wellfound['page_size'] == 10
    assert kinds['web.duckduckgo'] == 'html_search'
    assert kinds['directory.ycombinator.api'] == 'json_mapped'


def test_browser_source_requires_a_runtime_binding_before_creating_run_artifacts(tmp_path):
    request = tmp_path / 'request.json'
    request.write_text(json.dumps({'request': {'sources': [{'kind': 'browser_html_search'}]}}))
    run = tmp_path / 'run'
    with pytest.raises(ValueError, match='require --browser-runtime'):
        runtime.execute(request, run)
    assert not run.exists()


def test_wellfound_browser_directory_uses_caller_selected_pages():
    source = next(source for source in default_sources(pages=4, enable_browser=True) if source['id'] == 'marketplace.wellfound')
    assert source['pages'] == 4
    assert source['page_size'] == 10
    assert '{page}' in source['url_template']
    assert source['browser_mapping']['item_selector'] == 'div.rounded-lg.border.border-gray-400'


def test_request_can_select_pagination_and_explicit_read_budget(tmp_path):
    path = tmp_path / 'request.json'
    assert main(['init', '--objective', 'water filtration', '--pages', '3', '--enable-acquisition', '--max-requests', '75', '--output', str(path)]) == 0
    request = json.loads(path.read_text())['request']
    assert request['policy']['acquisition_enabled'] is True
    assert request['policy']['max_requests'] == 75
    paginated = [source for source in request['sources'] if source['pages'] == 3]
    assert paginated
    assert all(source['page_size'] > 0 for source in paginated if '{offset}' in source['url_template'])
    assert all('{page}' in source['url_template'] or '{offset}' in source['url_template'] or source.get('pagination') for source in paginated)
    assert request['criteria'] == []


def test_empty_catalog_is_an_explicit_opt_out(tmp_path):
    path = tmp_path / 'request.json'
    assert main(['init', '--objective', 'water filtration', '--empty-sources', '--output', str(path)]) == 0
    assert json.loads(path.read_text())['request']['sources'] == []


def test_init_rejects_invalid_page_or_read_budget(tmp_path):
    assert main(['init', '--objective', 'water filtration', '--pages', '0', '--output', str(tmp_path / 'pages.json')]) == 1
    assert main(['init', '--objective', 'water filtration', '--max-requests', '3', '--output', str(tmp_path / 'budget.json')]) == 1


def test_old_source_preset_flags_are_not_compatibility_aliases():
    with pytest.raises(SystemExit):
        main(['discover', '--source', 'community'])


def test_exact_printed_source_inventory():
    package, manifest = runtime.checked_manifest()
    assert manifest['process']['name'] == 'company_discovery.v1'
    assert manifest['stage'] == 'development'
    assert manifest['admitted'] is False
    assert (package / 'nodes/open/open/golang-x-net-LICENSE.txt').is_file()
    assert (package / 'process.json').is_file()


def test_changed_worker_source_refuses_before_build_or_execution(tmp_path, monkeypatch):
    package = tmp_path / 'package'
    shutil.copytree(runtime.package_path(), package)
    (package / 'worker/main.go').write_text('package changed')
    monkeypatch.setattr(runtime, 'package_path', lambda: package)
    with pytest.raises(ValueError, match='printed Process source changed'):
        runtime.checked_manifest()


def test_published_source_blocks_have_two_sites_per_population(tmp_path):
    from collections import Counter
    from career_fleet.channel_catalog import source_blocks_catalog
    entries = source_blocks_catalog()
    assert len(entries) == 12
    assert Counter(entry['family'] for entry in entries) == {
        'associations': 2, 'exhibitors': 2, 'suppliers': 2,
        'procurement': 2, 'geographic': 2, 'ecosystems': 2,
    }
    assert all(entry['kind'] == 'block' and entry['admitted'] is False for entry in entries)
    assert all(entry['block']['name'] and len(entry['block']['digest']) == 64 for entry in entries)
    path = tmp_path / 'request.json'
    assert main(['init', '--objective', 'business discovery', '--family', 'exhibitors', '--source', 'geographic.sf_chamber', '--pages', '2', '--output', str(path)]) == 0
    request = json.loads(path.read_text())['request']
    assert request['sources'] == [] and len(request['source_blocks']) == 3
    assert request['criteria'] == [] and not request['policy']['acquisition_enabled']
    assert all(selection['block']['digest'] for selection in request['source_blocks'])
    # Closed worker request validation checks the exact Block pins, including
    # caller paging changes, without contacting any source.
    from career_fleet.process_runtime import build_worker, execute
    build_worker()
    response, code = execute(path, tmp_path / 'run')
    assert code == 0 and response['ok']
    assert response['outputs']['result']['network_requests'] == 0


def test_source_selection_refuses_unknown_values(tmp_path):
    assert main(['init', '--objective', 'businesses', '--family', 'invented', '--output', str(tmp_path / 'bad.json')]) == 1
    assert not (tmp_path / 'bad.json').exists()


def test_printed_helper_closure_drift_refuses_before_packaging(tmp_path):
    import hashlib
    import subprocess
    import sys
    package = tmp_path / 'package'
    shutil.copytree(runtime.package_path(), package)
    helper = package / 'nodes/parse/parse/ocds.go'
    helper.write_bytes(helper.read_bytes() + b'\n')
    original = hashlib.sha256((runtime.package_path() / 'source-manifest.json').read_bytes()).hexdigest()
    repository = Path(__file__).resolve().parents[2]
    checked = subprocess.run([sys.executable, str(repository / 'scripts/package_discovery_process.py'), '--package', str(package)], capture_output=True, text=True)
    assert checked.returncode != 0 and 'printed Node closure changed: parse' in checked.stderr
    assert hashlib.sha256((runtime.package_path() / 'source-manifest.json').read_bytes()).hexdigest() == original


@pytest.mark.parametrize('tamper', ['reference', 'artifact', 'resource'])
def test_source_block_packaging_rejects_composition_drift(tmp_path, tamper):
    import hashlib
    import subprocess
    import sys
    package = tmp_path / 'package'
    shutil.copytree(runtime.package_path(), package)
    index_path = package / 'nodes/open/open/source_block_index.json'
    index = json.loads(index_path.read_text())
    (tmp_path / 'plan-result.json').write_text(json.dumps({
        'block_pins': {entry['key']: entry['block'] for entry in index['entries']},
    }))
    (tmp_path / 'blocks').mkdir()
    for entry in index['entries']:
        shutil.copyfile(package / entry['artifact'], tmp_path / 'blocks' / (entry['key'] + '.json'))
    entry = index['entries'][0]
    if tamper == 'reference':
        entry['block']['digest'] = '0' * 64
        index_path.write_text(json.dumps(index))
        reason = 'source Block reference differs from Forge composition'
    elif tamper == 'artifact':
        artifact = package / entry['artifact']
        block = json.loads(artifact.read_text())
        block['nodes'][0]['fills']['source']['id'] = 'changed-source'
        artifact.write_text(json.dumps(block))
        reason = 'source Block artifact differs from Forge composition'
    else:
        (package / 'nodes/open/open/golang-x-net-LICENSE.txt').write_text('changed attribution')
        reason = 'source-selection resource changed'
    original = hashlib.sha256((runtime.package_path() / 'source-manifest.json').read_bytes()).hexdigest()
    repository = Path(__file__).resolve().parents[2]
    checked = subprocess.run([sys.executable, str(repository / 'scripts/package_discovery_process.py'),
        '--package', str(package)], capture_output=True, text=True)
    assert checked.returncode != 0 and reason in checked.stderr
    assert hashlib.sha256((runtime.package_path() / 'source-manifest.json').read_bytes()).hexdigest() == original


def test_source_blocks_are_real_published_composition_and_executed_phases():
    from career_fleet.channel_catalog import source_blocks_catalog
    from career_fleet.process_runtime import checked_manifest
    package, _ = checked_manifest()
    compiled = json.loads((package / 'process.json').read_text())
    node_keys = {node['key'] for node in compiled['nodes']}
    for entry in source_blocks_catalog():
        block = json.loads((package / entry['artifact']).read_text())
        assert block['schema'] == 'aeltum.block.v2' and block['name'] == entry['block']['name']
        assert {node['key'] for node in block['nodes']} == {'boundrequest','capture','captureretain','bind','captureload','parse','inspect'}
        assert block['edges'] and block['inputs'] and block['outputs']
        bound = next(node for node in block['nodes'] if node['key']=='boundrequest')
        assert bound['fills']['source']['id'] == entry['id']
        prefix = str(len(entry['key'])) + ':' + entry['key']
        assert all(prefix + node['key'] in node_keys for node in block['nodes'])


def test_stale_source_block_refuses_before_network(tmp_path):
    from career_fleet import operations
    from career_fleet.channel_catalog import source_blocks_catalog
    request = operations.request_template('organizations')
    ref = dict(source_blocks_catalog()[0]['block'], digest='0'*64)
    request['source_blocks'] = [{'block':ref}]
    request['policy']['acquisition_enabled'] = True
    path=tmp_path/'stale.json';path.write_text(json.dumps({'request':request}))
    response,code=runtime.execute(path,tmp_path/'run')
    assert code != 0 and not response['ok'] and not response.get('outputs',{}).get('result')
