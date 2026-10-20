#!/usr/bin/env python3
"""Prepare and compose explicit source Blocks; Forge plan remains the printer."""
import argparse,copy,json,subprocess,os,hashlib
from pathlib import Path
ROOT=Path(__file__).resolve().parents[1]
P=ROOT/'processes/company-discovery'
def write(path,value):path.write_text(json.dumps(value,indent=2)+'\n')
def endpoint(node,port):return {'node':node,'port':port}
def edge(a,ap,b,bp):return {'from':endpoint(a,ap),'to':endpoint(b,bp)}
def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('stage',choices=['prepare','compose']);parser.add_argument('--library',type=Path);args=parser.parse_args()
 if args.stage=='prepare':
  directory=P/'nodes/boundrequest';directory.mkdir(exist_ok=True)
  (directory/'entry.go').write_text('''package boundrequest
import "fmt"
func Entry(in map[string]any)(map[string]any,error){var a struct{State State `json:"state"`;Source Source `json:"source"`};if e:=Decode(in,&a);e!=nil{return nil,e};s,e:=BindSourceBlock(a.State,a.Source);if e!=nil{return nil,e};if s.CurrentAction<0||s.CurrentAction>=len(s.Actions){return nil,fmt.Errorf("selected action absent")};return Output(&struct{State State `json:"state"`;URL string `json:"url"`}{s,s.Actions[s.CurrentAction].URL})}
''')
  spec=json.loads((P/'specs/request.json').read_text());spec['identity']['name']='discovery.bind_source_block.v1';spec['mechanism']['code']['pointer']='discovery.local/process/nodes/boundrequest.Entry';spec['composition']['inputs']['source']={'semantic':'discovery.source_definition.v1'}
  field=copy.deepcopy(spec['fields'][0]);field.update(name='source',type={'type':'object'},required=True);field['description'].update(intent='source definition',meaning='Immutable acquisition and extraction configuration supplied by the pinned Block fill.',source='Block definition');spec['fields'].append(field)
  spec['dimensions'][0]['contents']['inputs'].append({'field':'source','target':'source'});spec['usage']['purpose']='Bind the immutable source definition of a published Block before acquisition and extraction';write(P/'specs/boundrequest.json',spec)
  stores=json.loads((P/'stores.json').read_text())
  if not any(x.get('pointer')=='discovery.local/process/nodes/boundrequest.Entry' for x in stores['pointers']):stores['pointers'].append({'kind':'code','pointer':'discovery.local/process/nodes/boundrequest.Entry','path':'nodes/boundrequest/entry.go','digest':'0'*64})
  write(P/'stores.json',stores)
  # State is shared by the capture and its observational recovery, allowing an
  # offline selected capture to survive interruption without a network fallback.
  state=next(f for f in spec['fields'] if f['name']=='state' and f['side']=='input')
  for name in ['capture','capturerecover']:
   f=P/f'specs/{name}.json';s=json.loads(f.read_text())
   if not any(x['name']=='state' and x['side']=='input' for x in s['fields']):
    field=copy.deepcopy(state);field['required']=True;s['fields'].append(field);s['composition']['inputs']['state']={'semantic':'discovery.state.v1'};s['dimensions'][0]['contents']['inputs'].append({'field':'state','target':'param:state'})
   write(f,s)
  plan=json.loads((P/'plan.json').read_text())
  if not any(n['key']=='boundrequest' for n in plan['nodes']):plan['nodes'].append({'key':'boundrequest','spec_file':'specs/boundrequest.json','source_file':'nodes/boundrequest/entry.go'})
  for b in plan['blocks']:
   if b.get('key')=='acquire' and not any(e['to']==endpoint('capture','state') for e in b['edges']):b['edges'].append(edge('request','state','capture','state'))
  for binding in plan['process']['policy']['recovery']['bindings']:
   if binding['target']['node']=='capture':
    binding['inputs']['state']='state'
    block=binding['reconciler']['block']
    if not any(x['name']=='state' for x in block['inputs']):block['inputs'].append({'name':'state','to':endpoint('observe','state')})
  write(P/'plan.json',plan)
  inputs=json.loads((P/'author/source-block-inputs.json').read_text())
  blocks=[]
  for source in inputs['sources']:
   source=copy.deepcopy(source)
   key='source_'+source['id'].replace('.','_');source['block_key']=key
   nodes=[{'key':n,'node':n} for n in ['boundrequest','capture','captureretain','bind','captureload','parse','inspect']];nodes[0]['fills']={'source':source}
   blocks.append({'key':key,'name':'career.source.'+source['id']+'.v1','nodes':nodes,'inputs':[{'name':'state','to':endpoint('boundrequest','state')}], 'edges':[edge('boundrequest','state','capture','state'),edge('boundrequest','state','bind','state'),edge('boundrequest','url','capture','url'),edge('capture','capture','captureretain','capture'),edge('captureretain','capture','bind','capture'),edge('bind','state','captureload','state'),edge('captureload','state','parse','state'),edge('parse','state','inspect','state')], 'outputs':[{'name':'state','from':endpoint('inspect','state')},{'name':'next','from':endpoint('inspect','next')}]})
  write(P/'source-block-library-plan.json',{'schema':'forge.author_plan.v1','library':True,'module':plan['module'],'nodes':[n for n in plan['nodes'] if n['key'] in {'boundrequest','capture','captureretain','bind','captureload','parse','inspect'}],'blocks':blocks})
 else:
  if not args.library:parser.error('--library is required')
  library=args.library.resolve();result=json.loads((library/'plan-result.json').read_text());authored=json.loads((P/'source-block-library-plan.json').read_text());plan=json.loads((P/'plan.json').read_text());definitions=[];entries=[]
  plan['blocks']=[b for b in plan['blocks'] if not b['key'].startswith('source_')];plan['process']['blocks']=[b for b in plan['process']['blocks'] if not b['key'].startswith('source_')];plan['process']['progression']['phases']=[b for b in plan['process']['progression']['phases'] if not b['key'].startswith('source_')]
  recovery=plan['process']['policy']['recovery']['bindings'];recovery[:]=[b for b in recovery if not b['target']['block'].startswith('source_')]
  templates={b['target']['node']:b for b in recovery if b['target']['block'] in ['acquire','parse']}
  assets=[a for a in plan.get('assets',[]) if not a['rel_path'].startswith('source-blocks/') and a['rel_path']!='licenses/golang-x-net-LICENSE']
  resources=P/'nodes/open';material={}
  def retain(name,raw):
   (resources/name).write_bytes(raw);material[name]=hashlib.sha256(raw).hexdigest()
  retain('golang-x-net-LICENSE.txt',(P/'html-LICENSE').read_bytes())
  for block in authored['blocks']:
   key=block['key'];source=block['nodes'][0]['fills']['source'];ref=result['block_pins'][key];file=library/'blocks'/f'{key}.json'
   plan['blocks'].append({'key':key,'block_file':str(file)});plan['process']['blocks'].append({'key':key,'block':key});plan['process']['progression']['phases'].insert(-1,{'key':key,'block':key,'state_input':'state','state_output':'state','next_output':'next'})
   definitions.append({'Block':ref,'Key':key,'Source':source});entries.append({'id':source['id'],'kind':'block','key':key,'family':source['family'],'block':ref,'artifact':f'nodes/open/open/source_block_{key}.json','status':'development_unverified','admitted':False})
   retain(f'source_block_{key}.json',file.read_bytes())
   for node in ['capture','captureretain','captureload']:
    binding=copy.deepcopy(templates[node]);binding['target']={'block':key,'node':node};binding['reconciler']['key']=key+'_'+node+'_recovery';recovery.append(binding)
  spec_file=P/'specs/selectwork.json';spec=json.loads(spec_file.read_text());next_field=next(f for f in spec['fields'] if f['side']=='output' and f['name']=='next');next_field['type']['enum']=['acquire','acquire_browser','parse','done']+[b['key'] for b in authored['blocks']];write(spec_file,spec)
  lookup=P/'nodes/open/source_block_inventory.go';lookup.write_text('package open\nimport "encoding/json"\nfunc pinnedSourceBlocks() ([]SourceBlockDefinition,error) {var values []SourceBlockDefinition;e:=json.Unmarshal([]byte('+json.dumps(json.dumps(definitions,separators=(',',':')))+'),&values);return values,e}\n')
  index=P/'author/source-block-index.json';write(index,{'schema':'career_fleet.source_blocks.v1','entries':entries})
  retain('source_block_index.json',index.read_bytes())
  (resources/'source_block_material.go').write_text('package open\n// Source-selection metadata and attribution are part of this verified Node package.\nconst sourceBlockMaterialJSON = '+json.dumps(json.dumps(material,sort_keys=True,separators=(',',':')))+'\n')
  plan['assets']=assets;write(P/'plan.json',plan)
if __name__=='__main__':main()
