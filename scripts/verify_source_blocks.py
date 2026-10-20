#!/usr/bin/env python3
"""Run exact published source Blocks in the discovery Process; record evidence.

This command does not publish Blocks. Forge authors their pinned composition.
Public reads use the optional caller allowance supplied with --max-requests.
"""
import argparse,json
from pathlib import Path
from career_fleet import operations
from career_fleet.channel_catalog import source_blocks_catalog
from career_fleet.process_runtime import _atomic_json,checked_execution

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--evidence-directory',type=Path,required=True);parser.add_argument('--max-requests',type=int);parser.add_argument('--proof-output',type=Path);args=parser.parse_args()
 if args.max_requests is not None and args.max_requests<0:parser.error('--max-requests must be nonnegative')
 root=args.evidence_directory.resolve();root.mkdir(parents=True,exist_ok=True);reports=[];proofs=[]
 for entry in source_blocks_catalog():
  request=operations.request_template('Enumerate organizations explicitly named in this collection');request.update(mode='verify',source_blocks=[{'block':entry['block']}]);request['policy'].update(acquisition_enabled=True,follow_redirects=True)
  if args.max_requests is not None:request['policy']['max_requests']=args.max_requests
  path,run=root/(entry['id']+'.json'),root/entry['id'];payload={'request':request}
  if run.exists():
   checked_execution(run)
   if json.loads((run/'input.json').read_text())!=payload:raise ValueError('changed request requires new evidence directory')
   response=json.loads((run/'response.json').read_text())
  else:
   _atomic_json(path,payload);response,_=operations.discover(path,root/'research.db',run)
  result=response.get('outputs',{}).get('result',{});candidate=next((c for c in result.get('source_candidates',[]) if c.get('status')=='verified' and c.get('block')==entry['block']),None);listing=next((h for h in result.get('source_health',[]) if h.get('block')==entry['block'] and h['outcome']=='working' and h['records']>0),None)
  report={'id':entry['id'],'block':entry['block'],'verified':bool(candidate and listing),'event_id':response.get('event_id'),'run':str(run),'summary':result.get('summary',{}),'source_health':result.get('source_health',[]),'source_candidates':result.get('source_candidates',[]),'error':response.get('error')};reports.append(report);_atomic_json(root/'verification.json',{'blocks':reports});print(json.dumps({k:report[k] for k in ['id','block','verified','summary','error']}),flush=True)
  if candidate and listing:
   captures={c['id']:c for c in result['captures']};launch=checked_execution(run)
   proofs.append({'id':entry['id'],'block':entry['block'],'verification':{'event_id':response['event_id'],'process':launch['process'],'listing_records':listing['records'],'negative_mapping_check':candidate['negative_check'],'capture_digests':{key:captures[key]['sha256'] for key in candidate['capture_ids']},'evidence_directory':str(run),'scope':'Exact source Block executed: nonempty extraction, readable representative detail, missing-mapping refusal. Development evidence only.'}})
 passed=len(proofs)==len(reports) and bool(proofs)
 if args.proof_output:
  if not passed:raise ValueError('source Block checks failed; evidence index not updated')
  _atomic_json(args.proof_output,{'schema':'career_fleet.source_block_evidence.v1','entries':proofs})
 return 0 if passed else 1
if __name__=='__main__':raise SystemExit(main())
