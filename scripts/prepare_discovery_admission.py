#!/usr/bin/env python3
"""Author explicit proof vectors for existing Nodes; Forge owns proof execution.

Expectations below are authored postconditions, never copied from actual outputs.
Local capture reads select bytes retained by a real earlier Process invocation.
"""
import argparse
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
P = ROOT / 'processes/company-discovery'

def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2) + '\n')

def empty(schema):
    if schema['type'] == 'object':
        return {k: empty(schema['properties'][k]) for k in schema.get('required', [])}
    if schema['type'] == 'array': return []
    return {'string': '', 'integer': 0, 'boolean': False}[schema['type']]

def check(field, path, value=None, operator='equals'):
    out = {'field': field, 'path': path, 'operator': operator}
    if value is not None: out['value'] = value
    return out

def case(id, inputs, assertions=(), error=''):
    out = {'id': id, 'source': 'Independently authored discovery admission postcondition: '+id,
           'inputs': inputs}
    if error: out['error'] = error
    else: out['assertions'] = list(assertions)
    return out

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--seed-run', type=Path, required=True)
    parser.add_argument('--browser-runtime', type=Path, required=True)
    args = parser.parse_args()
    env = dict(os.environ, GOWORK='off')
    schemas = json.loads(subprocess.check_output(['go','run',str(ROOT/'scripts/discovery-schema/main.go')],cwd=P/'author/core',env=env))
    req = empty(schemas['request']); req['objective'] = 'industrial filtration'
    src = {'id':'collection','kind':'collection_html','preset':'html_table','record_kind':'organization',
        'url_template':'https://example.com/','queries':['{objective}'],'pages':1}
    req['sources'] = [src]; req['policy']['acquisition_enabled'] = True
    state = empty(schemas['state']); state.update(request=req,current_action=0,current_candidate=-1,phase='select')
    action_schema = schemas['state']['properties']['actions']['items']
    action = empty(action_schema); action.update(id='action:qualification',kind='search',root_kind='search',
        source_id='collection',url='https://example.com/',status='pending')
    state['actions'] = [action]
    obs_schema = schemas['state']['properties']['pending']['items']
    obs = empty(obs_schema); obs.update(id='observation:qualification',source_id='collection',capture_id='capture:qualification',
        url='https://example.com/acme',title='Acme Filters',text='We build ceramic filtration.',
        entity_name='Acme Filters',record_kind='organization',record_key='acme')
    body = '<html><table><tr><th>Company</th></tr><tr><td><a href="https://example.com/acme">Acme Filters</a></td></tr></table></html>'
    cap = empty(schemas['capture']); cap.update(id='capture:qualification',url='https://example.com/',
        final_url='https://example.com/',body_base64=base64.b64encode(body.encode()).decode(),
        sha256=hashlib.sha256(body.encode()).hexdigest(),status=200,media_type='text/html',outcome='success',complete=True,requests=1)
    def st(**kw):
        out=copy.deepcopy(state); out.update(copy.deepcopy(kw)); return out
    cases = {}
    cases['open'] = [case('success',{'request':req},[check('state','/phase','select'),check('state','/current_action',-1)]),
        case('held_out',{'request':dict(copy.deepcopy(req),objective='specialty materials')},[check('state','/request/objective','specialty materials')]),
        case('safe_failure',{'request':dict(copy.deepcopy(req),objective='',sources=[])},error='discovery: objective or collection required')]
    cases['selectwork'] = [case('success',{'state':st()},[check('next','','acquire'),check('state','/current_action',0)]),
        case('held_out',{'state':st(actions=[])},[check('next','','done')])]
    cases['request'] = [case('success',{'state':st()},[check('url','','https://example.com/')]),
        case('held_out',{'state':st(actions=[dict(action,url='https://example.org/')])},[check('url','','https://example.org/')])]
    cases['bind'] = [case('success',{'state':st(),'capture':cap},[check('state','/phase','parse'),check('state','/actions/0/capture_id',cap['id'])]),
        case('held_out',{'state':st(),'capture':dict(cap,id='capture:another')},[check('state','/capture/id','capture:another')])]
    cases['parse'] = [case('success',{'state':st(capture=cap)},[check('state','/document/results/0/entity_name','Acme Filters'),check('next','','evidence')]),
        case('held_out',{'state':st(capture=dict(cap,complete=False,outcome='transport_failure'))},[check('next','','select')])]
    cases['identity'] = [case('success',{'state':st(pending=[obs])},[check('state','/pending/0/entity_id','source:collection:acme'),check('state','/relationships',operator='non_empty')]),
        case('held_out',{'state':st(pending=[dict(obs,record_kind='document')])},[check('state','/pending/0/entity_id',''),check('state','/relationships',[])])]
    doc = empty(schemas['state']['properties']['document']);doc.update(outcome='parsed',health='working',results=[obs])
    cases['inspect'] = [case('success',{'state':st(capture=cap,document=doc)},[check('state','/source_health/0/outcome','working'),check('state','/source_health/0/records',1)]),
        case('held_out',{'state':st(capture=dict(cap,status=403),document=empty(schemas['state']['properties']['document']))},[check('state','/source_health/0/outcome','blocked')])]
    cases['merge'] = [case('success',{'state':st(pending=[obs])},[check('state','/candidates/0/name','Acme Filters'),check('state','/pending',[])]),
        case('held_out',{'state':st(pending=[obs,dict(obs,id='observation:other',record_key='other',entity_name='Other Filters',url='https://example.com/other')])},[check('state','/candidates/1/name','Other Filters')])]
    # Evidence stages must preserve sparse candidates and explicit unknowns.
    candidate_schema=schemas['state']['properties']['candidates']['items']
    candidate=empty(candidate_schema);candidate.update(id='lead:qualification',name='Acme Filters',observations=[obs])
    sparse=dict(candidate,id='lead:sparse',observations=[])
    concept={'id':'requested_material','terms':['ceramic'],'relations':[]}
    cases['match']=[case('success',{'state':st(candidates=[candidate],concepts=[concept])},
        [check('state','/candidates/0/matches/0/quote','ceramic'),check('state','/candidates/0/matches/0/observation_id',obs['id'])]),
        case('held_out',{'state':st(candidates=[sparse],concepts=[concept])},
        [check('state','/candidates/0/id','lead:sparse'),check('state','/candidates/0/matches',[])])]
    question=dict(empty(schemas['request']['properties']['questions']['items']),**{'id':'arbitrary_data','field':'operator_material_specification','scope':'product','acquisition':'source_document',
        'rules':[{'id':'material_span','field':'operator_material_specification','scope':'product','pattern':'build (ceramic) filtration','group':1}]})
    subject=dict(obs,entity_id='company:source-asserted',scope='product',field='source_result')
    selected_candidate=dict(candidate,observations=[subject])
    cases['extract']=[case('success',{'state':st(candidates=[selected_candidate],request=dict(copy.deepcopy(req),questions=[question]))},
        [check('state','/candidates/0/assertions/0/field','operator_material_specification'),check('state','/candidates/0/assertions/0/value','ceramic'),
         check('state','/candidates/0/assertions/0/quote','ceramic'),check('state','/candidates/0/assertions/0/start_byte',9),check('state','/candidates/0/assertions/0/end_byte',16),check('state','/candidates/0/assertions/0/observation_id',obs['id'])]),
        case('held_out',{'state':st(candidates=[dict(candidate,observations=[dict(subject,entity_id='')])],request=dict(copy.deepcopy(req),questions=[question]))},
        [check('state','/candidates/0/id','lead:qualification'),check('state','/candidates/0/assertions',[])])]
    criterion={'id':'selected_material','field':'operator_material_specification','scope':'product','role':'required','operator':'equals','value':'ceramic'}
    assertion=dict(empty(candidate_schema['properties']['assertions']['items']),id='assertion:authored',field='operator_material_specification',scope='product',value='ceramic',observation_id=obs['id'],source_id=obs['source_id'],capture_id=obs['capture_id'],url=obs['url'],entity_id=candidate['id'],quote='ceramic',start_byte=9,end_byte=16,rule_id='material_span')
    condition_request=dict(copy.deepcopy(req),criteria=[criterion])
    cases['conditions']=[case('success',{'state':st(candidates=[dict(candidate,assertions=[assertion])],request=condition_request)},
        [check('state','/candidates/0/conditions/0/state','supported'),check('state','/candidates/0/conditions/0/assertion_ids',['assertion:authored'])]),
        case('held_out',{'state':st(candidates=[sparse],request=condition_request)},
        [check('state','/candidates/0/id','lead:sparse'),check('state','/candidates/0/conditions/0/state','unknown'),check('state','/candidates/0/disposition','eligible')]),
        case('conflict',{'state':st(candidates=[dict(candidate,assertions=[assertion,dict(assertion,id='assertion:other',value='metal')])],request=condition_request)},
        [check('state','/candidates/0/conditions/0/state','conflict'),check('state','/candidates/0/disposition','eligible')])]
    other_question=dict(question,id='custom_revenue',field='operator_revenue_measure',scope='current',rules=[])
    cases['planwork']=[case('success',{'state':st(actions=[],candidates=[selected_candidate],request=dict(copy.deepcopy(req),questions=[question,other_question]))},
        [check('next','','select'),check('state','/actions/0/question_id','arbitrary_data'),check('state','/actions/1/question_id','custom_revenue'),check('state','/actions/0/candidate_id','lead:qualification')]),
        case('held_out',{'state':st(actions=[],candidates=[dict(selected_candidate,identity_state='unresolved')],request=dict(copy.deepcopy(req),questions=[other_question,question]))},
        [check('state','/actions/0/question_id','custom_revenue'),check('state','/actions/1/question_id','arbitrary_data')])]
    cases['done']=[case('success',{'state':st(candidates=[candidate])},[check('disposition','','complete'),check('result','/candidates/0/id','lead:qualification')]),
        case('held_out',{'state':st(candidates=[])},[check('result','/candidates',[])])]
    bound=dict(copy.deepcopy(src),block_key='qualification_collection')
    selected=dict(copy.deepcopy(bound),block={'name':'qualification.collection','digest':'a'*64})
    block_state=st(request=dict(copy.deepcopy(req),sources=[selected]))
    cases['boundrequest']=[case('success',{'state':block_state,'source':bound},[check('url','','https://example.com/')]),
        case('held_out',{'state':block_state,'source':dict(bound,preset='auto_collection')},error='discovery: source definition differs from pinned Block configuration')]
    digest='b'*64
    for key in ['capture','capturerecover','browsercapture','browsercapturerecover']:
        browser=key.startswith('browser')
        values=[]
        for id,url in [('success','https://example.com/'),('held_out','https://example.org/')]:
            s=st(actions=[dict(action,url=url)])
            if browser:s['request']['sources']=[dict(src,kind='browser_html_search',preset='')]
            inputs={'state':s,'url':url}
            if 'recover' in key:inputs['request_digest']=digest
            assertions=[check('capture','/url',url),check('capture','/outcome','success'),check('capture','/complete',True)]
            if 'recover' in key:assertions.append(check('request_digest','',digest))
            values.append(case(id,inputs,assertions))
        bad={'url':'http://127.0.0.1/','state':st()}
        if 'recover' in key:bad['request_digest']=digest
        error=('discovery: browser recovery requires an absolute public HTTPS URL' if 'recover' in key else 'discovery: browser capture requires an absolute public HTTPS URL') if browser else 'discovery: authorized source must be an absolute HTTPS URL without credentials'
        values.append(case('safe_failure',bad,error=error))
        cases[key]=values
    seed=args.seed_run.resolve(); seed_result=json.loads((seed/'response.json').read_text())['outputs']['result']
    retained=seed_result['captures'][0]
    record=next((seed/'evidence').rglob(retained['sha256']+'.json'))
    encoded=record.read_text()
    assert hashlib.sha256(encoded.encode()).hexdigest()==retained['body_record_digest']
    inline=dict(retained,body_base64=encoded);inline.pop('body_record',None);inline.pop('body_record_digest',None)
    cases['captureretain']=[case('success',{'capture':cap},[check('capture','/body_record',cap['sha256']),check('capture','/body_base64','')]),
        case('held_out',{'capture':dict(cap,body_base64='',outcome='transport_failure')},[check('capture','/outcome','transport_failure')])]
    cases['captureload']=[case('success',{'state':st(capture=retained)},[check('state','/capture/sha256',retained['sha256']),check('state','/capture/body_base64',operator='non_empty')]),
        case('held_out',{'state':st(capture=dict(retained,body_record_digest='0'*64))},error='capture body record unavailable or changed')]
    cases['retainrecover']=[case('success',{'capture':inline,'request_digest':digest},[check('capture','/body_record',retained['sha256']),check('status','','completed')]),
        case('held_out',{'capture':dict(inline,body_base64='Y2hhbmdlZA=='),'request_digest':digest},error='retained capture differs from original')]
    cases['loadrecover']=[case('success',{'state':st(capture=retained),'request_digest':digest},[check('state','/capture/body_base64',operator='non_empty'),check('status','','completed')]),
        case('held_out',{'state':st(capture=dict(retained,body_record_digest='0'*64)),'request_digest':digest},error='capture body record unavailable or changed')]
    # Codify positive held-out inputs separately from expected refusals.
    # The factory compares complete errors and all declared successful outputs.
    cases['bind'][0]['assertions'].append(check('next','','parse'))
    cases['inspect'][0]['assertions'].append(check('next','','evidence'))
    for c in cases['inspect']: c['inputs']['state']['phase']='evidence'
    cases['request'][0]['assertions'].append(check('state','/current_action',0))
    cases['boundrequest'][0]['assertions'].append(check('state','/current_action',0))
    cases['done'][0]['assertions'].append(check('state','/candidates/0/id','lead:qualification'))
    for key in ['capturerecover','browsercapturerecover','retainrecover','loadrecover']:
        cases[key][0]['assertions'].extend([check('request_digest','',digest),check('status','','completed')])
    for key in ['boundrequest','captureload','retainrecover','loadrecover']:
        old=next(c for c in cases[key] if c['id']=='held_out')
        old['id']='changed_selection_refused'
        if key=='boundrequest':
            other=copy.deepcopy(block_state);other['actions'][0]['query']='different caller query'
            inputs={'state':other,'source':bound};assertions=[check('url','','https://example.com/'),check('state','/actions/0/query','different caller query')]
        elif key=='retainrecover':
            inputs={'capture':dict(inline,id='capture:held-out'),'request_digest':digest}
            assertions=[check('capture','/id','capture:held-out'),check('capture','/body_record',retained['sha256']),check('request_digest','',digest),check('status','','completed')]
        else:
            inputs={'state':st(capture=retained,phase='parse')}
            assertions=[check('state','/phase','parse'),check('state','/capture/body_base64',operator='non_empty')]
            if key=='loadrecover':inputs['request_digest']=digest;assertions.extend([check('request_digest','',digest),check('status','','completed')])
        cases[key].append(case('held_out',inputs,assertions))
    plan=json.loads((P/'plan.json').read_text())
    for node in plan['nodes']:
        key=node['key']; directory=P/'nodes'/key;spec_path=P/node['spec_file'];spec=json.loads(spec_path.read_text())
        values=cases[key]
        if not any(c['id']=='safe_failure' for c in values):
            field='capture' if key in ['captureretain','retainrecover'] else 'state'
            bad={field:'invalid'}
            if key=='bind':bad['capture']=cap
            if key=='boundrequest':bad['source']=bound
            if 'recover' in key:bad['request_digest']=digest
            # Typed proof input validation refuses this before Entry decoding.
            values.append(case('safe_failure',bad,error='$ must be object'))
        if key=='open':bad={'request':'invalid'}
        elif key.startswith('browser'):bad={'url':''}
        elif 'recover' in key:bad={'request_digest':''}
        else:bad={('capture' if key=='captureretain' else 'state'):'invalid'}
        call='Entry('+('nil, ' if spec['identity']['kind']=='integration' else '')+'in)'
        test='package '+key+'\nimport("testing";"encoding/json")\nfunc TestProof(t *testing.T){var in map[string]any;if e:=json.Unmarshal([]byte(`'+json.dumps(bad,separators=(',',':'))+'`),&in);e!=nil{t.Fatal(e)};if _,e:='+call+';e==nil{t.Fatal("malformed input produced a result")}}\n'
        test_path=directory/'proof_test.go';test_path.write_text(test)
        spec['proof_spec'].update(behavior=values,test={'pointer':f'discovery.local/process/nodes/{key}.TestProof','digest':hashlib.sha256(b'praxis.test.go.v1\0'+test.encode()).hexdigest()})
        public_requests=0
        for dimension in spec.get('dimensions',[]):
            if dimension['name']=='egress' and dimension['contents']['mode'] in ['https','browser']:
                public_requests=sum(op.get('max_requests',0) for op in dimension['contents']['operations'])*4
        spec['composition']['tuning']={
            'schema':'aeltum.node_tuning.v1','mode':'fixed','replay':'captured_inputs','parameters':{},
            'capture_inputs':[f['name'] for f in spec['fields'] if f['side']=='input'],
            'checks':[{'id':c['id'],'kind':'safe_failure' if c.get('error') else 'success' if c['id']=='success' else 'held_out',
                'purpose':c['source'],'outputs':sorted({a['field'] for a in c.get('assertions',[])})} for c in values],
            'limits':{'max_trials':1,'max_seconds':600,'max_requests':public_requests,'max_spend_micros':0},'live_proof':'proof_spec'}
        write(spec_path,spec);node['test_file']=f'nodes/{key}/proof_test.go'
    write(P/'plan.json',plan)
    write(P/'author/qualification/node-cases.json',cases)
    write(P/'author/qualification/execution-inputs.json',{'capture_directory':str(seed/'evidence'),'browser':json.loads(args.browser_runtime.read_text())})
    print('Authored proof vectors for',len(cases),'Node definitions; no admission executed.')

if __name__=='__main__':main()
