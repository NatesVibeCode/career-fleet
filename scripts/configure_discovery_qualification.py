#!/usr/bin/env python3
"""Bind reviewed requirements and independently authored cases to the plan.

Run after publishing/recomposing source Blocks and refreshing exact assessments.
Node behavior and all business fields stay in the authored Process, not the host.
"""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import shutil

ROOT=Path(__file__).resolve().parents[1]
P=ROOT/'processes/company-discovery'
PURPOSES={
 'open':'Validate caller-selected sources, arbitrary requested data and prior evidence without personal defaults or a fixed enrichment order.',
 'selectwork':'Choose the next authorized finite acquisition or finish, preserving sparse organizations and caller allowances.',
 'request':'Bind the selected explicit acquisition URL to its carried task state.',
 'boundrequest':'Refuse a source definition that differs from the exact source Block fill before acquisition.',
 'capture':'Retain the exact outcome and bytes of one authorized public GET or selected prior capture.',
 'browsercapture':'Retain a pinned anonymous browser read and its response/accounting evidence.',
 'captureretain':'Publish the selected capture bytes to the scoped local artifact store.',
 'bind':'Associate one exact capture with its selected action without silently changing identity or request counts.',
 'captureload':'Load only capture bytes whose retained record and content digests match the selected capture.',
 'parse':'Extract typed source records and explicit coverage gaps from the selected response bytes.',
 'inspect':'Expose source health, extraction failures, repeated-page termination and verification outcomes.',
 'identity':'Retain source-asserted organization identities and relationships without merging businesses by shared names or domains.',
 'merge':'Retain every discovered observation while preserving distinct organization identities.',
 'match':'Retain lexical relevance with original spans and explicit unknowns independently of factual conditions.',
 'extract':'Extract whichever caller-requested fields have explicit evidence without assigning fields to enrichment rungs.',
 'conditions':'Evaluate only the caller-selected conditions and keep missing evidence explicitly unknown.',
 'planwork':'Plan caller-selected questions for any listed business and supported field without requiring a previous enrichment rung.',
 'done':'Deliver all retained businesses, evidence, requested data, unknowns and coverage through the typed public result.',
}

def write(p,x):p.write_text(json.dumps(x,indent=2)+'\n')
def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--evidence-root',type=Path,required=True)
 args=parser.parse_args();root=args.evidence_root.resolve();root.mkdir(parents=True,exist_ok=True)
 plan=json.loads((P/'plan.json').read_text())
 data=json.loads((P/'author/qualification/execution-inputs.json').read_text())
 capture=Path(data['capture_directory']).resolve()
 browser={k:str(Path(v).resolve()) for k,v in data['browser'].items()}
 browser_work=root/'browser-state';browser_work.mkdir(exist_ok=True)
 paths={'CAREER_CAPTURE_STORE':{'path':str(capture),'kind':'write_path'},'CAREER_BROWSER_STATE':{'path':str(browser_work),'kind':'write_path'}}
 for name,key in [('CAREER_BROWSER_NODE','node_executable'),('CAREER_PLAYWRIGHT','playwright_directory'),('CAREER_BROWSER_BUNDLE','browser_directory')]:
  paths[name]={'path':browser[key],'kind':'read_path'}
 for n in plan['nodes']:
  spec=json.loads((P/n['spec_file']).read_text());key=n['key']
  material={'assessment_file':f'author/qualification/assessments/{key}.json','license':'Career Fleet owned source; selected tokenizer license retained'}
  if spec['identity']['kind']=='function':material['freezes']=[]
  else:
   policy=next(d['contents'] for d in spec['dimensions'] if d['name']=='egress')
   operations={}
   for op in policy['operations']:
    binding={'operation':op['id']}
    if policy['mode']=='record_store':binding['directory']=str(capture)
    if policy['mode']=='browser':binding.update(directory=str(browser_work),browser=browser)
    operations[op['id']]=binding
   material['operations']=operations
  n['qualification']=material
 # This authored requirement template describes outcomes reviewed above. Its
 # explicit instances follow the selected business Blocks, not observed output.
 coverage=[];members={}
 for block in plan['blocks']:
  key=block['key']
  if key.startswith('source_'):
   node_keys=['boundrequest','capture','captureretain','bind','captureload','parse','inspect']
  else:node_keys=[n['key'] for n in block['nodes']]
  members[key]=node_keys
  for node in node_keys:
   deliveries=[{'node_output':'capture' if node in ['capture','browsercapture','captureretain'] else 'state','process_output':'state'}]
   if node=='done':deliveries.append({'node_output':'result','process_output':'result'})
   coverage.append({'id':key+'_'+node,'purpose':PURPOSES[node], 'owner':{'block':key,'node':node},'node_outcome':'completed','deliveries':deliveries})
 plan['process']['policy']['acceptance']={'schema':'praxis.process_outcome_coverage.v1','required_outcomes':coverage}
 # Every success case independently constrains both public outputs. The data
 # itself may change; these rules require completion, visible source records,
 # zero inference, and the exact caller objective rather than a frozen dataset.
 def req():
  from career_fleet.operations import request_template
  x=request_template('Enumerate businesses and preserve arbitrary requested data')
  x['policy'].update(acquisition_enabled=True,follow_redirects=True)
  return x
 def requirements(case_id,conditions):
  return {'schema':'aeltum.requirements.v1','rules':[{'id':case_id+'_postcondition','purpose':'Deliver the independently requested discovery outcome.', 'rationale':'A source-bound run must complete and expose its actual result without inferred qualification or a fixed data ladder.', 'origin':'explicit','source':'Career Fleet company-first discovery contract','cases':[case_id], 'mode':'runtime','missing':'error','condition':{'op':'all','args':conditions}}]}
 def success(id,request,blocks):
  needed=[c['id'] for c in coverage if c['owner']['block'] in blocks]
  visits=[{'block':b,'node':n} for b in blocks for n in members[b]]
  return {'case':{'schema':'praxis.process_case.v1','id':id,'event_id':'career-admission-'+id,'expected_ok':True,'inputs':{'request':request},'output_requirements':{
   'state':requirements(id,[{'op':'equals','path':'/phase','value':'done'},{'op':'equals','path':'/request/objective','value':request['objective']}]),
   'result':requirements(id,[{'op':'equals','path':'/inference_calls','value':0},{'op':'gte','path':'/summary/source_records','value':1}])}},'requirements':needed,'must_visit':visits}
 cases=[]
 index=json.loads((P/'author/source-block-index.json').read_text())
 for entry in index['entries']:
  request=req();request['source_blocks']=[{'block':entry['block']}]
  cases.append(success(entry['key'],request,['open','select',entry['key'],'evidence','done']))
 # The same real member listing exercises both generic acquisition paths.
 # Its mapping is explicit input here; these cases do not inject source Blocks.
 directory=json.loads((P/'author/source-block-inputs.json').read_text())['sources'][0]
 for browser_case in [False,True]:
  request=req();source=copy.deepcopy(directory);source['id']='public_member_directory'
  if browser_case:
   source['kind']='browser_html_search';source['browser_mapping']=source.pop('html_mapping');source.pop('preset',None)
   source['readiness']={'results_selector':'.card--member','empty_selector':'','blocked_selector':'','timeout_ms':10000}
  request['sources']=[source]
  id='generic_browser' if browser_case else 'generic_static'
  cases.append(success(id,request,['open','select','acquire_browser' if browser_case else 'acquire','parse','evidence','done']))
 # Representative prior-list entries exercise arbitrary data requests through
 # the whole Process. example.com is a public document, not a business claim.
 vectors=json.loads((P/'author/qualification/node-cases.json').read_text())
 prior=copy.deepcopy(vectors['planwork'][0]['inputs']['state']['candidates'][0])
 prior.update(id='entity:public-example',name='Example Domain',identity_state='unresolved',assertions=[])
 observation=prior['observations'][0]
 observation.update(id='prior:public-example',lead_id=prior['id'],source_id='public-example',capture_id='',url='https://example.com/',title='Example Domain',text='',field='source_result',entity_id='',entity_name='',scope='',record_kind='mention',record_key='public-example')
 template=vectors['extract'][0]['inputs']['state']['request']['questions'][0]
 questions=[]
 for id,field,pattern in [('subject','operator_subject_term','(domain)'),('notice','operator_notice','(documentation)')]:
  question=copy.deepcopy(template);question.update(id=id,field=field,scope='public-example',source_id='')
  question['rules']=[{'id':id+'_span','field':field,'scope':'public-example','pattern':pattern,'group':1}]
  questions.append(question)
 for id,order,refresh in [('fields_any_order',questions,False),('fields_reversed',list(reversed(questions)),False),('refresh_prior_field',[questions[0]],True)]:
  request=req();entry=copy.deepcopy(prior)
  if refresh:
   assertion=copy.deepcopy(vectors['conditions'][0]['inputs']['state']['candidates'][0]['assertions'][0]);assertion.update(field='operator_subject_term',scope='public-example',value='stale prior value')
   entry['assertions']=[assertion]
  request.update(prior=[entry],questions=copy.deepcopy(order),subject_bindings=[{'url':'https://example.com/','entity_id':'public-example','entity_name':'Example Domain','scope':'public-example'}])
  selected=success(id,request,['open','select','acquire','parse','evidence','done'])
  rules=selected['case']['output_requirements']['result']['rules'][0]['condition']['args']
  rules.append({'op':'equals','path':'/candidates/0/id','value':prior['id']})
  for i,question in enumerate(order):
   rules.extend([{'op':'equals','path':f'/candidates/0/assertions/{i}/field','value':question['field']},{'op':'equals','path':f'/candidates/0/assertions/{i}/value','value':'domain' if question['id']=='subject' else 'documentation'}])
  cases.append(selected)
 bad=req();bad['source_blocks']=[{'block':{'name':'invalid','digest':'0'*64}}]
 cases.append({'case':{'schema':'praxis.process_case.v1','id':'stale_block','event_id':'career-admission-stale-block','expected_ok':False,'inputs':{'request':bad},'expected_error':'discovery: unknown or stale source Block invalid@'+'0'*64},'requirements':['open_open']})
 native_workspace=root/'native-ao.work'
 native_workspace.write_text('go 1.26.0\n\nuse (\n'+''.join('  '+json.dumps(str(Path('/Users/nate/Private Repos/Repositories/Platform')/name))+'\n' for name in ['harness-sdk','Outcome Engine','AOA','ABAC'])+')\n')
 helper=Path(shutil.which('forge-qualification-owner')).resolve()
 plan['qualification']={'native_workspace':str(native_workspace),'registry':str(root/'registry'),'atlas_root':'/Users/nate/Private Repos/Repositories/Praxis/atlas','ao_root':'/Users/nate/Private Repos/Repositories/Platform/harness-sdk','owner_helper':{'file':str(helper),'sha256':hashlib.sha256(helper.read_bytes()).hexdigest()},'outcome_id':'career.business_discovery', 'owner_limits':{'timeout_millis':600000,'no_output_limit':True,'max_output_bytes':0,'max_active':1},'runtime':{'network':'outbound','bindings':{},'public_read':{'owner_reference':'operator:career-fleet-public-discovery','paths':paths}},'completion_cases':[x['case']['id'] for x in cases if x['case']['expected_ok']],'cases':cases}
 write(P/'plan.json',plan)
 print('Prepared',len(coverage),'outcome requirements and',len(cases),'independent Process cases.')

if __name__=='__main__':main()
