package main

import (
	contractschema "discovery.local/process/internal/contractschema"
	valuecontract "discovery.local/process/internal/valuecontract"
	executionjournal "discovery.local/process/internal/executionjournal"
	requirements "discovery.local/process/internal/requirements"
	egress "discovery.local/process/internal/egresscontract"
	"context"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nodeEntry struct {
	infer func(egress.InferClient, map[string]any) (map[string]any, error)
	scoped func(egress.Client, map[string]any) (map[string]any, error)
	scopedConfig func(egress.Client, string, map[string]any) (map[string]any, error)
	harness func(string, map[string]any) (map[string]any, map[string]any, error)
	run  func(inputs map[string]any) (map[string]any, error)
	runConfig func(config string, inputs map[string]any) (map[string]any, error)
	config string
	spec string
}

type nodeRef struct { name, digest string }
func (n processNode) reference() nodeRef { return nodeRef{name:n.node,digest:n.digest} }
func readNodeSpec(path string) ([]byte,error) {
 raw,err:=os.ReadFile(path);if err!=nil{return nil,err}
 var spec map[string]any
 if err:=decodeProcessJSON(raw,&spec);err!=nil{return nil,err}
 for ref,entry:=range dispatch{if entry.spec==path{
  digest,ok:=canonDigest(spec,"praxis.spec.v1")
  if !ok || digest!=ref.digest{return nil,fmt.Errorf("printed Node declaration changed: %s",path)}
  return raw,nil
 }}
 return nil,fmt.Errorf("unbound printed Node declaration: %s",path)
}

// driverReceipts holds the type-specific execution receipt of the last
// harness dispatch per node, so node receipts and loop round records keep
// the harness label and its closed receipt. Never shared between an infer
// and a harness path.
var driverReceipts sync.Map

// typedFailureRecord is the optional structural contract a Node error may
// implement to preserve its machine-readable failure identity. Only exported
// methods are used, so a pure compiled Function Node satisfies it without
// importing the worker or any execution package. It never authorizes an
// outcome: unknown errors stay technical failures with honest unknown
// identity, and a record never turns a failure into a business gap.
type typedFailureRecord interface {
	FailureRecord() map[string]any
}

// detachedJSONValue copies one record value as an exact detached JSON value:
// numbers keep their exact tokens (UseNumber) so revisions above 2^53 survive,
// and the copy shares nothing with the caller's mutable value. Malformed,
// unencodable or null values are refused.
func detachedJSONValue(value any) (any, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, false
	}
	if out == nil {
		return nil, false
	}
	return out, true
}

// failureFields builds the digest-bound failure record carried by results,
// activation receipts and node receipts. Display is always the original error
// text. Node, code and detail are copied only as non-empty strings; identity
// may be either a non-empty string or a structured value, which is preserved
// as a detached exact JSON copy (revisions above 2^53 keep their exact
// tokens). Only these four keys are copied: arbitrary top-level record keys
// are excluded so they cannot leak into receipts. A record whose method
// panics, is not a map, or carries unencodable identity stays an honest
// technical unknown.
func failureFields(err error) map[string]any {
	out := map[string]any{"display": "<nil>"}
	if err != nil {
		out["display"] = err.Error()
	}
	var record map[string]any
	var typed typedFailureRecord
	if errors.As(err, &typed) && typed != nil {
		func() {
			defer func() {
				if recover() != nil {
					record = nil
				}
			}()
			record = typed.FailureRecord()
		}()
	}
	for _, key := range []string{"node", "code", "detail"} {
		if value, ok := record[key].(string); ok && value != "" {
			out[key] = value
		}
	}
	if value, present := record["identity"]; present {
		if text, ok := value.(string); ok {
			if text != "" {
				out["identity"] = text
			}
		} else if detached, ok := detachedJSONValue(value); ok {
			out["identity"] = detached
		}
	}
	if _, ok := out["code"]; !ok {
		out["code"] = "runtime.failure.unknown"
	}
	if _, ok := out["identity"]; !ok {
		out["identity"] = "unknown"
	}
	return out
}

// injectRuntimeInputs adds a Node's resolved runtime inputs to a digest or
// execution input map so the checkpoint request identity covers them.
func injectRuntimeInputs(nodes []processNode, key string, inputs map[string]any) {
	for _, node := range nodes {
		if node.key != key {
			continue
		}
		for name, value := range node.runtimeFills {
			if _, exists := inputs[name]; !exists {
				inputs[name] = value
			}
		}
	}
}

// runtimeReceiptDigest recomputes the exact executed input identity for one
// runtime-bound ordinary Node, including the injected runtime values.
func runtimeReceiptDigest(nodes []processNode, edges []processEdge, results map[string]map[string]any, key string) (string, error) {
	inputs, err := nodeInputs(nodes, edges, results, key)
	if err != nil {
		return "", err
	}
	injectRuntimeInputs(nodes, key, inputs)
	return recoveryInputDigest(inputs)
}

// runtimeFillsFor resolves one Node's intrinsic runtime inputs from the event
// identity and the deterministic activation sequence. Missing event identity
// or an unknown authored source refuses before the Node runs; the values are
// host/process-owned and are never taken from model content or caller input.
func runtimeFillsFor(n processNode, runtimeValues map[string]string, sequence int) (map[string]any, error) {
	if len(n.runtime) == 0 {
		return nil, nil
	}
	out := map[string]any{}
	for field, source := range n.runtime {
		switch source {
		case "event_id", "invocation_id", "process_revision":
			value, ok := runtimeValues[source]
			if !ok || value == "" {
				return nil, fmt.Errorf("runtime input %s requires event identity", field)
			}
			out[field] = value
		case "receipt_replacement_producer", "receipt_replacement_custody", "activation_context", "settled_checkpoint":
            // Resolved from actual phase preparation by the private kernel.
            continue
		case "invocation_authority", "settled_failures", "continuation_status":
 var status map[string]any;if err:=decodeProcessJSON([]byte(runtimeValues[source]),&status);err!=nil{return nil,err};out[field]=status
 case "sequence":
			out[field] = sequence
		case "event":
			eventID, ok := runtimeValues["event_id"]
			if !ok || eventID == "" {
				return nil, fmt.Errorf("runtime input %s requires event identity", field)
			}
			invocationID, ok := runtimeValues["invocation_id"]
			if !ok || invocationID == "" {
				return nil, fmt.Errorf("runtime input %s requires invocation identity", field)
			}
			out[field] = map[string]any{"event_id": eventID, "invocation_id": invocationID, "sequence": sequence}
		default:
			return nil, fmt.Errorf("runtime input source %q is unsupported", source)
		}
	}
	return out, nil
}

func main() {
	if err := run(); err != nil {
		if _,ok:=err.(reportedFailure);ok {os.Exit(1)}
		result := map[string]any{"ok": false, "error": err.Error(), "failure": failureFields(err)}
		if failure,ok:=err.(*processFailure);ok {result["process"]=failure.process;result["source"]=failure.source;result["node"]=failure.node;result["composition"]=failure.composition}
		out, _ := json.Marshal(result)
		printDevelopmentOutcome(out)
		os.Exit(1)
	}
}

func run() (runErr error) {
	start := time.Now()
	f, err := flags(os.Args[1:])
	if err != nil {
		return err
	}
	if f.cwd != "" {
		if err := os.Chdir(f.cwd); err != nil {
			return err
		}
	}
	if f.help {
		printWorkerHelp()
		return nil
	}
	comp, dirs, err := compositionDigest(f.process)
	if err != nil {
		return err
	}
	if f.evGet != "" {
		return evidenceGet(comp, f.deployEnv, f.evGet)
	}
	if f.seal { return fmt.Errorf("development packages cannot seal credentials or claim admission") }
if false {
		return sealRun(comp, dirs, f.tokenEnvs, f.deployEnv)
	}
	if len(f.tokenEnvs) > 0 {
		return fmt.Errorf("credential grants are the only run-time credential path; -token-env is valid only with -seal")
	}
	dep, err := loadDeployment()
	if err != nil {
		return err
	}
	sealedBounds, err := loadSealedBounds()
	if err != nil {
		return err
	}
	deployEnv := f.deployEnv
	if deployEnv == "" && dep != nil {
		deployEnv = dep.dataKey
	}
	raw, err := os.ReadFile(f.process)
	if err != nil {
		return err
	}
	var processDoc map[string]any
	if err := decodeProcessJSON(raw, &processDoc); err != nil {
		return err
	}
	nodes, err := processNodes(processDoc)
	if err != nil {
		return err
	}
	if err := bindHarnessArms(nodes); err != nil {
		return err
	}
	if f.resume != "" {
		record, parked, err := loadParkedRound(f.resume)
		if err != nil {
			return err
		}
		if status, _ := parked["status"].(string); status != "awaiting_approval" {
			return fmt.Errorf("resume round is not awaiting approval")
		}
		if f.approvalTokenEnv == "" {
			return fmt.Errorf("resume needs -approval-token-env NAME; values never come from disk")
		}
		record.TokenEnv = f.approvalTokenEnv
		harnessResume = record
	}
	if f.authority {return describeAuthority(comp,processDoc,nodes)}
	edges := processEdges(processDoc)
	if dep != nil {
		if p,ok:=processDoc["process"].(map[string]any);ok&&p["policy"]!=nil&&len(dep.fills)>0{return fmt.Errorf("process configuration requires a new configured Process revision")}
		for key, fills := range dep.fills {
			known := false
			for i := range nodes {
				if nodes[i].key == key {
					known = true
				}
			}
			if !known {
				return fmt.Errorf("deployment fill node %s: unknown node", key)
			}
			for field := range fills {
				for _, e := range edges {
					if e.toNode == key && e.toField == field {
						return fmt.Errorf("deployment fill %s.%s conflicts with an edge", key, field)
					}
				}
			}
		}
		for i := range nodes {
			if fills, ok := dep.fills[nodes[i].key]; ok {
				for field, value := range fills {
					nodes[i].fills[field] = value
				}
			}
		}
	}
	boundary, err := bindProcess(processDoc, nodes, f, dep, edges)
	if err != nil { return err }
	var eventClaim *executionjournal.Journal
	// A refusal after boundary binding retains its actual Process context.
	// Event execution is claimed only after beginEvent acquired real custody;
	// this does not complete, publish or authorize recovery of failed work.
	defer func(){
		if runErr==nil || boundary==nil{return};if _,reported:=runErr.(reportedFailure);reported{return}
		result:=map[string]any{"ok":false,"error":runErr.Error(),"failure":failureFields(runErr),"process":boundary.ref,"exposure":boundary.exposure,"composition":comp}
		var sourceFailure *processFailure
		if errors.As(runErr,&sourceFailure){result["source"]=sourceFailure.source;result["node"]=sourceFailure.node}
		if eventClaim!=nil{result["event_id"]=f.eventID;result["event_payload_digest"]=eventClaim.PayloadDigest();result["execution_mode"]="executed_event";if eventClaim.Resumed(){result["execution_mode"]="resumed_event"}}
		raw,e:=marshalWorkerResponse(result);if e!=nil{return};fmt.Println(string(raw));runErr=reportedFailure{}
	}()
 if err:=validateRuntimePortBindings(nodes,edges);err!=nil{return err}
	inv, err := checkTriggerGate(comp, dep, nodes, f)
	if err != nil {
		return err
	}
	specs := map[string][]map[string]any{}
	kinds := map[string]string{}
	for _, n := range nodes {
		entry, ok := dispatch[n.reference()]
		if !ok {
			return fmt.Errorf("node %s: unknown node %s", n.key, n.node)
		}
		fields, err := specFields(entry.spec)
		if err != nil {
			return fmt.Errorf("node %s: %v", n.key, err)
		}
		specs[n.key] = fields
		kind, err := specIdentityKind(entry.spec)
		if err != nil {
			return fmt.Errorf("node %s: %v", n.key, err)
		}
		kinds[n.key] = kind
	}
	loops, err := parseWorkerLoops(processDoc, nodes, kinds)
	if err != nil {
		return err
	}
	progression, err := parseWorkerProgression(processDoc, nodes, edges, specs, kinds)
	if err != nil {
		return err
	}
	if boundary!=nil {if err:=validateRootBindings(boundary,progression,nodes,f);err!=nil{return err}}
	if progression != nil && boundary == nil {
		return fmt.Errorf("progression requires a compiled Tool or Flow")
	}
	// Progression and the separately bounded recovery policy are two
	// mechanisms with no defined precedence together; the model and the
	// portable verifier refuse the combination, and the worker refuses it
	// here rather than choosing an order.
	
	loopNodes := map[string]bool{}
	for _, loop := range loops {
		loopNodes[loop.DriverKey] = true
		for _, tool := range loop.Tools {
			loopNodes[tool] = true
		}
	}
	for _, e := range edges {
		if loopNodes[e.fromNode] || loopNodes[e.toNode] {
			return fmt.Errorf("loop nodes do not participate in process edges: %s", e.toNode)
		}
	}
	scheduled := make([]processNode, 0, len(nodes))
	for _, n := range nodes {
		if !loopNodes[n.key] && !n.recoveryOnly {
			scheduled = append(scheduled, n)
		}
	}
	bv, err := resolveBounds(sealedBounds, dep, nodes, specs)
	if err != nil {
		return err
	}
	if err := initPillScope(dep, nodes, edges, specs, boundary); err != nil {
		return err
	}
	if boundary != nil {
		// Check every supplied input before any parallel leaf can have effects.
		// Pill blanks with a declared late slot skip this pre-check: the late
		// binder verifies them against slot, pin, and receipt at execution.
		// A blank without a declared slot refuses here, before any effect.
		for _,n:=range nodes {for _,field:=range specs[n.key] {
			if field["side"]!="input" {continue};name,_:=field["name"].(string)
			if value,ok:=n.fills[name];ok {
				if isPillAddress(value) {
					if _,declared:=pillSlotFor(n.key,name);!declared {return &processFailure{boundary.ref,boundary.sources[n.key],n.key,comp,fmt.Errorf("pill blank without declared late slot")}}
					continue
				}
				if err:=checkValue(field,value,bv.forKey(n.key,"input",name));err!=nil {return &processFailure{boundary.ref,boundary.sources[n.key],n.key,comp,err}}}
		}}
	}
	levels, err := topo(scheduled, edges)
	if err != nil {
		return err
	}
	authenticationFrame,err:=readInvocationAuthority(nodes,boundary,f)
	if err!=nil{return err}
	eventClaim,cached,err:=beginEvent(comp,nodes,boundary,f,deployEnv)
	if err!=nil {return err}
	if eventClaim!=nil {defer eventClaim.Close()}
	if f.executionControl!="" {return runExecutionControl(eventClaim,boundary,f)}
	if cached!=nil {
		var retained map[string]any
		if err:=decodeProcessJSON(cached.Response,&retained);err!=nil{return err}
		retained["execution_mode"]="retained_event"
		raw,err:=marshalWorkerResponse(retained);if err!=nil{return err};printDevelopmentOutcome(raw)
		if cached.Failed{return reportedFailure{}};return nil
	}
	beginNodeTimings(progression == nil && len(loops) == 0)
	results := map[string]map[string]any{}
	failures:=map[string]string{}
	unknowns:=map[string]string{}
	// typedFailures keeps the full digest-bound record beside the display
	// strings, so the loop API and existing readers are unchanged.
	typedFailures:=map[string]map[string]any{}
	recovery:=newRecoveryRun(boundary,eventClaim)
 recovery.settledCheckpoints,err=settledCheckpointBindings(processDoc);if err!=nil{return err}
	// Intrinsic runtime inputs are generated here: the event identity comes
	// from the execution flags and the invocation identity is the exact
	// digest of composition, event and payload. A Node that declares runtime
	// inputs requires an execution event; there is no constant emulation.
	runtimeValues:=map[string]string{}
 if boundary!=nil{runtimeValues["process_revision"],_=boundary.ref["digest"].(string)}
	runtimeNeeded:=progression!=nil && len(progression.Events)>0
	for _,n:=range nodes{if len(n.runtime)!=0{runtimeNeeded=true}}
	if runtimeNeeded {
		if eventClaim==nil {return fmt.Errorf("runtime inputs require event identity and execution-owned state")}
		runtimeValues["event_id"]=f.eventID
		invocationDigest,ok:=canonDigest(map[string]any{"composition":comp,"event_id":f.eventID,"payload":eventClaim.PayloadDigest()},"praxis.invocation_id.v1")
		if !ok {return fmt.Errorf("invocation identity undigestible")}
		runtimeValues["invocation_id"]=invocationDigest
	}
	if authenticationFrame!=nil {authenticationFrame["worker_invocation_id"]=runtimeValues["invocation_id"];raw,e:=json.Marshal(authenticationFrame);if e!=nil{return e};runtimeValues["invocation_authority"]=string(raw)}
	recovery.runtimeValues=runtimeValues
	var loopBodies []map[string]any
	var activationBodies []map[string]any
	var capabilities map[string]egress.InvocationHandle
 var continuation *continuationRun
 if progression!=nil{var e error;continuation,e=beginContinuation(progression.Continuation,boundary,f,deployEnv,eventClaim);if e!=nil{return e};defer continuation.close();if continuation!=nil{raw,e:=json.Marshal(continuation.status);if e!=nil{return e};runtimeValues["continuation_status"]=string(raw)}}
	if progression != nil {
		if eventClaim==nil {return fmt.Errorf("progression requires event identity and execution-owned state directory")}
		activationBodies, err = runWorkerProgression(progression, nodes, edges, specs, bv, recovery, comp, boundary, f, deployEnv, results, failures, unknowns, typedFailures, runtimeValues, continuation)
		if err != nil && !retainProgressionFailure(err, activationBodies, failures, typedFailures) { return err }
	} else {
	if runtimeNeeded {
		ordinal:=0
		ordinals:=map[string]int{}
		for _,level:=range levels {
			ordered:=append([]string(nil),level...);sort.Strings(ordered)
			for _,key:=range ordered {
				ordinal++
				ordinals[key]=ordinal
				for i:=range nodes {
					if nodes[i].key!=key {continue}
					fills,err:=runtimeFillsFor(nodes[i],runtimeValues,ordinal);if err!=nil{return err}
					nodes[i].runtimeFills=fills
				}
			}
		}
		recovery.activationOrdinals=ordinals
	}
	capabilities, err = prepareEgress(comp, boundary, nodes, f, deployEnv)
	if err != nil { return err }
	defer cancelEgressHandles(capabilities)
	for _, level := range levels {
		// A failed producer blocks its descendants, not independent branches.
		// Keep the original levels for receipts; skipped nodes remain not_run.
		ready := make([]string, 0, len(level))
		for _, key := range level {
			blocked := false
			for _, edge := range edges {
				if edge.toNode == key {
					if _, succeeded := results[edge.fromNode]; !succeeded {
						blocked = true
						break
					}
				}
			}
			if !blocked { ready = append(ready, key) }
		}
		level = ready
		snap := map[string]map[string]any{}
		for k, v := range results {
			snap[k] = v
		}
		type outcome struct {
			key string
			out map[string]any
			err error
		}
		ch := make(chan outcome, len(level))
		var wg sync.WaitGroup
		for _, key := range level {
			wg.Add(1)
			go func(key string) {
				defer wg.Done()
				out, err := func() (out map[string]any, err error) {
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "node %s panic stack:\n%s\n", key, debug.Stack())
							out = nil
							err = fmt.Errorf("adapter panic: %v", r)
						}
					}()
					return recovery.execute(nodes, edges, specs, snap, key, bv, capabilities)
				}()
				ch <- outcome{key: key, out: out, err: err}
			}(key)
		}
		wg.Wait()
		close(ch)
		outcomes:=make([]outcome,0,len(level));for o:=range ch {outcomes=append(outcomes,o)}
		// Panics arrive as outcome values via the recovery above, but a
		// goroutine that exits without sending (runtime.Goexit) still
		// shortens the channel: refuse rather than run on partial results.
		if len(outcomes)!=len(level){
			cause:=fmt.Errorf("node execution stopped without a reported outcome; inspect before retrying")
			if boundary!=nil{reported:=map[string]bool{};for _,o:=range outcomes{reported[o.key]=true};missing:=append([]string(nil),level...);sort.Strings(missing);for _,key:=range missing{if !reported[key]{return &processFailure{boundary.ref,boundary.sources[key],key,comp,cause}}}}
			return cause
		}
		sort.Slice(outcomes,func(i,j int)bool{return outcomes[i].key<outcomes[j].key})
		for _,o := range outcomes {
			if o.err != nil {
				if fatal,ok:=o.err.(*checkpointFailure);ok {return fatal}
				if unknown,ok:=o.err.(*recoveryUnknown);ok {fields:=failureFields(unknown);unknowns[o.key]=fields["display"].(string);typedFailures[o.key]=fields;continue}
				if park,ok:=o.err.(*harnessApproval);ok {
					// A prompting customer harness parks explicitly: never a
					// hang, never a silent proceed, never a wrapped failure.
					out,_:=json.Marshal(map[string]any{"ok":false,"status":"awaiting_approval","round":park.parked,"harness_receipt":park.receipt})
					printDevelopmentOutcome(out)
					return reportedFailure{}
				}
				if boundary != nil || len(capabilities)>0 {
					fields:=failureFields(o.err);failures[o.key]=fields["display"].(string);typedFailures[o.key]=fields;continue
				}
				return fmt.Errorf("node %s: %w", o.key, o.err)
			}
			results[o.key] = o.out
		}
	}
	recovery.probeResults.Range(func(key,value any)bool{results[key.(string)]=value.(map[string]any);return true})
	recovery.probeFailures.Range(func(key,value any)bool{fields:=value.(map[string]any);failures[key.(string)]=fields["display"].(string);typedFailures[key.(string)]=fields;return true})
	loopBodies, err = runWorkerLoops(loops, nodes, specs, results, bv, capabilities, failures)
	if err != nil {
		if park, ok := err.(*harnessApproval); ok {
			out, _ := json.Marshal(map[string]any{"ok": false, "status": "awaiting_approval", "round": park.parked, "harness_receipt": park.receipt})
			printDevelopmentOutcome(out)
			return reportedFailure{}
		}
		return err
	}
	}
	order := []string{}
	if progression == nil {
	for _, level := range levels {
		cp := append([]string(nil), level...)
		sort.Strings(cp)
		order = append(order, cp...)
	}
	for _,n:=range nodes{if n.recoveryOnly{order=append(order,n.key)}}
	}
	receipts := make([]any, 0, len(order))
	if err := checkRunBounds(sealedBounds, dep, time.Since(start)); err != nil {
		return err
	}
	// The receipt chain roots at composition + declaration digest:
	// provenance terminates at the operator's authored document when
	// one shipped; without one, the composition is the root.
	prev := comp
	declarationDigest:=""
	receiptProjection:=""
	if process,ok:=processDoc["process"].(map[string]any);ok{if policy,ok:=process["policy"].(map[string]any);ok{receiptProjection,_=policy["receipt_projection"].(string)}}
	if declRaw, err := os.ReadFile("declaration.json"); err == nil {
		var declaration map[string]json.RawMessage
 if json.Unmarshal(declRaw,&declaration)==nil && receiptProjection==""{_ = json.Unmarshal(declaration["receipt_projection"],&receiptProjection)}
 sum := sha256.Sum256(declRaw)
		declarationDigest=hex.EncodeToString(sum[:])
		rooted, ok := canonDigest(map[string]any{
			"composition": comp, "declaration": hex.EncodeToString(sum[:]),
		}, "praxis.receipt.root.v1")
		if !ok {
			return fmt.Errorf("receipt root undigestible")
		}
		prev = rooted
	}
	if eventClaim!=nil {
		var ok bool
		prev,ok=canonDigest(map[string]any{"prev":prev,"event_id":f.eventID,"payload":eventClaim.PayloadDigest()},"praxis.event_receipt_root.v1")
		if !ok{return fmt.Errorf("event receipt root undigestible")}
	}
	for _, key := range order {
		norm := normJSON(results[key])
		body:=map[string]any{"node":key,"outputs":norm,"prev":prev};if observation,ok:=nodeInvocationObservations.Load(key);ok{body["invocation"]=observation}
        if timing,ok:=nodeTimings.Load(key);ok {body["timing"]=normJSON(timing)}
		if handle:=capabilities[key];handle!=nil {var receipts any=handle.Receipt();if multi,ok:=handle.(interface{Receipts() []egress.Receipt});ok{receipts=multi.Receipts()};raw,err:=json.Marshal(receipts);if err!=nil{return err};receipt,err:=valuecontract.Decode(raw);if err!=nil{return err};body["egress"]=receipt}
		if raw,ok:=driverReceipts.Load(key);ok {body["driver"]=map[string]any{"type":"harness","receipt":raw}}
		if evidence,ok:=recovery.evidence.Load(key);ok {delete(body,"egress");delete(body,"driver");for name,value:=range evidence.(map[string]any){body[name]=value}}
		if execution,ok:=recovery.origins.Load(key);ok {body["execution"]=execution
		} else if _,succeeded:=results[key];succeeded {
			for _,candidate:=range nodes {
				if candidate.key!=key||len(candidate.runtime)==0 {continue}
				digest,err:=runtimeReceiptDigest(nodes,edges,results,key);if err!=nil{return err}
				body["execution"]=map[string]any{"origin":"executed","request_digest":digest}
			}
		}
		if checks:=receiptRequirements(key,nodes,edges,specs,results);len(checks)>0{body["requirements"]=checks}
		if binds,ok:=pillBindRecords.Load(key);ok {body["pill_binds"]=binds}
		if boundary!=nil || len(capabilities)>0 {
			state:="not_run";if _,ok:=results[key];ok {state="succeeded"};if message,ok:=failures[key];ok {state="failed";body["error"]=message}
			if message,ok:=unknowns[key];ok{state="unknown";body["error"]=message}
			if record,ok:=typedFailures[key];ok{body["failure"]=record}
			body["state"]=state;if boundary!=nil {body["source"]=boundary.sources[key];body["process"]=boundary.ref}
		}
		d, ok := canonDigest(body, "praxis.receipt.v1")
		if !ok {
			return fmt.Errorf("node %s: receipt undigestible", key)
		}
		receipt := body;receipt["digest"]=d
		receipts = append(receipts, receipt)
		prev = d
	}
	// Activation receipts own the chain for a progressing Process: every
	// activation is immutable, scoped to its exact identity, and linked to
	// the predecessor (or the event receipt root for the first activation).
	for _, body := range activationBodies {
		body["prev"] = prev
		d, ok := canonDigest(body, "praxis.activation_receipt.v1")
		if !ok {
			return fmt.Errorf("activation receipt undigestible")
		}
		body["digest"] = d
		receipts = append(receipts, body)
		prev = d
	}
	loopRecords := make([]any, 0, len(loopBodies))
	for _, loopBody := range loopBodies {
		loopBody["prev"] = prev
		d, ok := canonDigest(loopBody, "praxis.loop_receipt.v1")
		if !ok {
			return fmt.Errorf("loop receipt undigestible")
		}
		receipt := loopBody;receipt["digest"]=d
		receipts = append(receipts, receipt)
		loopRecords = append(loopRecords, receipt)
		prev = d
	}
	result := map[string]any{
		"composition": comp, "nodes": results,
		"ok": true, "receipts": receipts,
	}
	if continuation!=nil{result["continuation"]=continuation.evidence()}
 if continuation.isPending(){result["status"]="pending"}
 if len(loopRecords) > 0 {result["loops"] = loopRecords}
	if progression!=nil {result["activation_count"]=len(activationBodies)}
	if boundary!=nil && (boundary.recovery!=nil || boundary.progression) && declarationDigest!=""{result["declaration_digest"]=declarationDigest}
	if eventClaim!=nil {result["event_id"]=f.eventID;result["event_payload_digest"]=eventClaim.PayloadDigest();result["execution_mode"]="executed_event"}
	// A validated frontier terminal may deliver useful pending outputs while
	// original child failures remain sealed. This is neither head publication
	// nor successful settlement of that work. Unknown outcomes retain refusal.
	pendingDelivery := boundary != nil && progression != nil && progression.Frontier != nil && continuation.isPending() && len(unknowns) == 0
	if len(failures)>0 {keys:=make([]string,0,len(failures));for key:=range failures {keys=append(keys,key)};sort.Strings(keys);result["ok"]=false;result["node"]=keys[0];result["error"]=failures[keys[0]];if record,ok:=typedFailures[keys[0]];ok{result["failure"]=record}}
	if len(unknowns)>0 {keys:=make([]string,0,len(unknowns));for key:=range unknowns {keys=append(keys,key)};sort.Strings(keys);result["ok"]=false;result["status"]="outcome_unknown";result["error"]="process outcome remains unknown; unresolved Nodes require reconciliation";if record,ok:=typedFailures[keys[0]];ok{result["failure"]=record}}
	if boundary != nil {
		result["process"]=boundary.ref;result["exposure"]=boundary.exposure
		if pendingDelivery || len(failures)==0 && len(unknowns)==0 {outputs,err:=boundary.project(results);if err!=nil {return err};result["outputs"]=outputs}
		if len(failures)>0 {
			keys:=make([]string,0,len(failures));for key:=range failures {keys=append(keys,key)};sort.Strings(keys);key:=keys[0]
			result["ok"]=false;result["error"]=failures[key];result["node"]=key;result["source"]=boundary.sources[key];if record,ok:=typedFailures[key];ok{result["failure"]=record}
		}
	}
	if pendingDelivery { result["ok"] = true }
	if eventClaim!=nil && eventClaim.Resumed(){result["execution_mode"]="resumed_event"}
	if len(unknowns)>0 {keys:=make([]string,0,len(unknowns));for key:=range unknowns {keys=append(keys,key)};sort.Strings(keys);result["ok"]=false;result["status"]="outcome_unknown";result["error"]="process outcome remains unknown; unresolved Nodes require reconciliation";if record,ok:=typedFailures[keys[0]];ok{result["failure"]=record}}
	out, err := marshalWorkerResponse(result)
	if err != nil {
		return err
	}
	if dep != nil && dep.evidence != "" {
		if err := persistEvidence(dep, comp, deployEnv, inv, out); err != nil {
			return err
		}
	}
	// Freeze exact private execution bytes before public projection mutates receipt maps.
	privateExecutionJSON:=string(out)
	if boundary!=nil && (boundary.recovery!=nil || boundary.progression) && !f.diagnostics {delete(result,"nodes");delete(result,"continuation");out,err=marshalWorkerResponse(result);if err!=nil{return err}}
	if boundary!=nil && (boundary.recovery==nil || receiptProjection=="public_process_outputs") && f.tool!="" && !f.diagnostics {
		delete(result,"nodes")
		for _,entry:=range receipts {receipt:=entry.(map[string]any);delete(receipt,"outputs");delete(receipt,"error");delete(receipt,"failure");if execution,ok:=receipt["execution"].(map[string]any);ok{delete(execution,"settled_failure_capsule");delete(execution,"receipt_replacement");delete(execution,"target_checkpoint");delete(execution,"resolution")}}
		delete(result,"failure")
		if boundary.recovery==nil && len(failures)>0 {result["error"]="process execution failed; inspect diagnostic receipts before retrying"}
 if receiptProjection=="public_process_outputs" {result["diagnostic_outputs_omitted"]=true}
		out,err=marshalWorkerResponse(result);if err!=nil {return err}
	}
	if eventClaim!=nil && continuation.isPending() { if err:=continuation.recordPendingDelivery(out,privateExecutionJSON,len(failures)>0||len(unknowns)>0);err!=nil{return fmt.Errorf("pending delivery not recorded; retained work is unchanged: %w",err)} }
	if eventClaim!=nil && len(unknowns)==0 && !continuation.isPending() {if err:=eventClaim.Complete(executionjournal.Outcome{Response:out,ExecutionResponseJSON:privateExecutionJSON,Failed:len(failures)>0});err!=nil{return fmt.Errorf("event completion not recorded; inspect before retrying: %w",err)}}
	printDevelopmentOutcome(out)
	if len(unknowns)>0 || len(failures)>0 && !pendingDelivery {return reportedFailure{}}
	return nil
}

// Preserve the actual failed activation chain for an ordinary Node refusal.
// Setup, custody, approval and unresolved outcomes still leave through their
// original refusal path; an error without a matching activation is not evidence.
func retainProgressionFailure(err error, records []map[string]any, failures map[string]string, typedFailures map[string]map[string]any) bool {
 if err==nil{return false}
 var checkpoint *checkpointFailure
 var unknown *recoveryUnknown
 var approval *harnessApproval
 if errors.As(err,&checkpoint)||errors.As(err,&unknown)||errors.As(err,&approval){return false}
 matched:=false
 for _,record:=range records {
  state,_:=record["state"].(string)
  if state!="succeeded"&&state!="failed"{return false}
  if state!="failed"{continue}
  key,_:=record["node"].(string)
  activation,_:=record["activation"].(string)
  message,_:=record["error"].(string)
  failure,ok:=record["failure"].(map[string]any)
  if key==""||activation==""||message==""||!ok||failure["display"]!=message{return false}
  if message==err.Error(){matched=true}
 }
 if !matched{return false}
 for _,record:=range records {
  if record["state"]!="failed"{continue}
  key:=record["node"].(string)
  failures[key]=record["error"].(string)
  typedFailures[key]=record["failure"].(map[string]any)
 }
 return true
}

// JSON receipts carry text as UTF-8; redundant HTML escaping is unnecessary.
// Receipt hashes still bind the same decoded values and request digests.
func marshalWorkerResponse(value any)([]byte,error) {
 var b bytes.Buffer
 encoder:=json.NewEncoder(&b);encoder.SetEscapeHTML(false)
 if err:=encoder.Encode(value);err!=nil{return nil,err}
 return bytes.TrimSuffix(b.Bytes(),[]byte("\n")),nil
}

type processNode struct {
	receiptKey string
	recoveryOnly bool
	configuration *string
	digest string
	key   string
	node  string
	fills map[string]any
	runtime map[string]string
	runtimeFills map[string]any
}

type processEdge struct {
	fromNode  string
	fromField string
	toNode    string
	toField   string
	pill      map[string]any
}

type cliFlags struct {
 executionControl string
 controlSequence uint64
 controlReason string
 invocationAuthorityFD int
 invocationAuthorityPackageDigest string
	eventID string
	eventState string
	providerHTTPSPipe bool
	providerHTTPSPackageDigest string
	publicReadPipe bool
	publicReadPackageDigest string
	authority bool
	egressBindings string
	credentialGrants string
	credentialMode string
	process      string
	tokenEnvs     []string
	seal          bool
	deployEnv     string
	trigger       string
	triggerSigEnv string
	evGet         string
	input         string
	tool          string
	flow          string
	diagnostics   bool
	help          bool
	cwd           string
	resume        string
	approvalTokenEnv string
}

func flags(args []string) (cliFlags, error) {
	var f cliFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-provider-https-pipe": f.providerHTTPSPipe=true
		case "-public-read-pipe": f.publicReadPipe=true
		case "-authority": f.authority=true
		case "-diagnostics": f.diagnostics=true
		case "-h", "-help", "--help": f.help=true
		case "-invocation-authority-fd", "-invocation-authority-package-digest", "-process", "-token-env", "-deployment-key-env", "-trigger", "-trigger-sig-env", "-evidence-get", "-input", "-tool", "-flow", "-credential-grants", "-credential-mode", "-egress-bindings", "-event-id", "-event-state-dir", "-provider-https-package-digest", "-public-read-package-digest", "-cwd", "-resume", "-approval-token-env", "-execution-control", "-control-sequence", "-control-reason":
			if i+1 >= len(args) {
				return cliFlags{}, fmt.Errorf("flag %s needs a value", args[i])
			}
			which := args[i]
			i++
			switch which {
 case "-invocation-authority-fd": value,e:=strconv.Atoi(args[i]);if e!=nil || value<3{return cliFlags{},fmt.Errorf("invalid invocation authority descriptor")};f.invocationAuthorityFD=value
 case "-invocation-authority-package-digest": f.invocationAuthorityPackageDigest=args[i]
			case "-execution-control": f.executionControl=args[i]
 case "-control-sequence": var err error;f.controlSequence,err=strconv.ParseUint(args[i],10,64);if err!=nil{return cliFlags{},fmt.Errorf("control sequence must be an unsigned integer")}
 case "-control-reason": f.controlReason=args[i]
 case "-event-id": f.eventID=args[i]
			case "-cwd": f.cwd=args[i]
			case "-resume": f.resume=args[i]
			case "-approval-token-env": f.approvalTokenEnv=args[i]
			case "-event-state-dir": f.eventState=args[i]
			case "-provider-https-package-digest": f.providerHTTPSPackageDigest=args[i]
			case "-public-read-package-digest": f.publicReadPackageDigest=args[i]
			case "-egress-bindings": f.egressBindings=args[i]
			case "-credential-grants": f.credentialGrants=args[i]
			case "-credential-mode": f.credentialMode=args[i]
			case "-input": f.input=args[i]
			case "-tool": f.tool=args[i]
			case "-flow": f.flow=args[i]
			case "-process":
				f.process = args[i]
			case "-deployment-key-env":
				f.deployEnv = args[i]
			case "-trigger":
				f.trigger = args[i]
			case "-trigger-sig-env":
				f.triggerSigEnv = args[i]
			case "-evidence-get":
				f.evGet = args[i]
			case "-token-env":
				f.tokenEnvs = append(f.tokenEnvs, args[i])
			default:
				return cliFlags{}, fmt.Errorf("unhandled flag %s", which)
			}
		case "-seal":
			f.seal = true
		default:
			return cliFlags{}, fmt.Errorf("unknown flag %s", args[i])
		}
	}
	if f.help {
		return f, nil
	}
	if f.process == "" {
		return cliFlags{}, fmt.Errorf("need -process PATH [-seal [-token-env key=NAME]] [-deployment-key-env NAME] [-trigger ID -trigger-sig-env NAME] [-evidence-get KEY]")
	}
	if (f.eventID=="")!=(f.eventState=="") {return cliFlags{},fmt.Errorf("event identity and execution-owned state directory must be supplied together")}
	if f.providerHTTPSPipe != (f.providerHTTPSPackageDigest!="") || (f.providerHTTPSPipe && f.publicReadPipe) {return cliFlags{},fmt.Errorf("provider HTTPS pipe requires exclusive sealed package transport")}
	if f.providerHTTPSPipe {if !strings.HasPrefix(f.providerHTTPSPackageDigest,"sha256:") || len(f.providerHTTPSPackageDigest)!=71 {return cliFlags{},fmt.Errorf("provider HTTPS package digest is invalid")};if _,err:=hex.DecodeString(strings.TrimPrefix(f.providerHTTPSPackageDigest,"sha256:"));err!=nil{return cliFlags{},fmt.Errorf("provider HTTPS package digest is invalid")}}
	if f.publicReadPipe != (f.publicReadPackageDigest!="") {return cliFlags{},fmt.Errorf("public-read pipe and sealed package identity must be supplied together")}
	if f.publicReadPipe {if !strings.HasPrefix(f.publicReadPackageDigest,"sha256:") || len(f.publicReadPackageDigest)!=71 {return cliFlags{},fmt.Errorf("public-read package digest is invalid")};if _,err:=hex.DecodeString(strings.TrimPrefix(f.publicReadPackageDigest,"sha256:"));err!=nil{return cliFlags{},fmt.Errorf("public-read package digest is invalid")}}
	if (f.invocationAuthorityFD!=0)!=(f.invocationAuthorityPackageDigest!=""){return cliFlags{},fmt.Errorf("invocation authority descriptor and package identity required together")}
 if f.invocationAuthorityFD!=0 {if !invocationAuthorityDigest(f.invocationAuthorityPackageDigest,true){return cliFlags{},fmt.Errorf("invalid invocation authority package identity")};if f.eventID=="" || f.seal || f.authority || f.evGet!="" {return cliFlags{},fmt.Errorf("invocation authority requires event execution")}}
	if f.executionControl!="" {if f.eventID=="" || (f.executionControl!="inspect" && f.executionControl!="cancel"){return cliFlags{},fmt.Errorf("execution control requires an exact event and inspect or cancel")};if f.executionControl=="cancel" && (f.controlSequence==0 || strings.TrimSpace(f.controlReason)==""){return cliFlags{},fmt.Errorf("cancel requires caller sequence and reason")};if f.executionControl=="inspect" && (f.controlSequence!=0 || f.controlReason!=""){return cliFlags{},fmt.Errorf("inspect cannot supply control facts")}} else if f.controlSequence!=0 || f.controlReason!="" {return cliFlags{},fmt.Errorf("control facts require execution control")}
 if f.eventID!="" && (f.seal||f.authority||f.evGet!="") {return cliFlags{},fmt.Errorf("event identity is only valid for Tool/Flow execution")}
	return f, nil
}

func printWorkerHelp() {
	fmt.Println("worker -process process.json [-tool NAME | -flow NAME] [-input INPUT.json | -]")
	fmt.Println("")
	fmt.Println("Verifies the forge seal, executes one Tool or Flow exposure with the")
	fmt.Println("given JSON object input, and prints the result object with receipts.")
	fmt.Println("Run from the printed root, or pass -cwd DIR to select it.")
	fmt.Println("Use -input - to pipe the input object on standard input.")
	fmt.Println("")
	fmt.Println("Deployment:")
	fmt.Println("  1. Copy the printed directory as one unit (process.json, nodes/, worker/).")
	fmt.Println("  2. Run: worker -process process.json -tool NAME -input input.json")
	fmt.Println("  3. Verify only, without executing: worker -process process.json -authority")
	fmt.Println("  4. Seal node credentials: worker -process process.json -seal -deployment-key-env NAME -token-env key=ENV")
	fmt.Println("")
	fmt.Println("Run-time credentials require -credential-grants; -token-env is")
	fmt.Println("valid only with -seal, where it names seal inputs, not run-time values.")
	fmt.Println("")
	fmt.Println("Flags: -process PATH -tool NAME -flow NAME -input PATH|- -cwd DIR")
	fmt.Println("  -deployment-key-env NAME -trigger ID -trigger-sig-env NAME")
	fmt.Println("  -evidence-get KEY -credential-grants PATH -credential-mode MODE")
	fmt.Println("  -egress-bindings PATH -event-id ID -event-state-dir DIR")
	fmt.Println("  -public-read-pipe -public-read-package-digest sha256:DIGEST (controller transport)")
	fmt.Println("  -resume ROUND_ID -approval-token-env NAME (values never on disk)")
	fmt.Println("  -seal -authority -diagnostics -h")
}

func readInputBytes(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func strMap(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}

func processNodes(processDoc map[string]any) ([]processNode, error) {
	raw, ok := processDoc["nodes"].([]any)
	if !ok {
		return nil, fmt.Errorf("process nodes shape")
	}
	var out []processNode
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("process node shape")
		}
		key, _ := m["key"].(string)
		node, _ := strMap(m, "node")["name"].(string)
		if key == "" || node == "" {
			return nil, fmt.Errorf("process node key/node")
		}
		n := processNode{key: key, node: node, fills: map[string]any{}}
		if raw,present:=m["recovery_only"];present{flag,ok:=raw.(bool);if !ok{return nil,fmt.Errorf("recovery_only must be boolean")};n.recoveryOnly=flag}
		n.digest,_=strMap(m,"node")["digest"].(string)
  if len(n.digest)!=64{return nil,fmt.Errorf("process Node requires an exact revision")};if _,err:=hex.DecodeString(n.digest);err!=nil{return nil,fmt.Errorf("process Node revision invalid")}
 if raw,exists:=m["configuration"]; exists { value,ok:=raw.(string); if !ok { return nil,fmt.Errorf("node configuration must be an explicit string") }; n.configuration=&value }
		if f := strMap(m, "fills"); f != nil {
			n.fills = f
		}
		if rawRuntime := strMap(m, "runtime"); len(rawRuntime) != 0 {
			n.runtime = map[string]string{}
			for field, value := range rawRuntime {
				source, ok := value.(string)
				if !ok || field == "" || source == "" {
					return nil, fmt.Errorf("process node runtime shape")
				}
				n.runtime[field] = source
			}
		}
		out = append(out, n)
	}
	return out, nil
}

func processEdges(processDoc map[string]any) []processEdge {
	raw, _ := processDoc["edges"].([]any)
	var out []processEdge
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		from := strMap(m, "from")
		to := strMap(m, "to")
		e := processEdge{}
		e.fromNode, _ = from["node"].(string)
		e.fromField, _ = from["field"].(string)
		e.toNode, _ = to["node"].(string)
		e.toField, _ = to["field"].(string)
		e.pill, _ = m["pill"].(map[string]any)
		out = append(out, e)
	}
	return out
}

func specFields(path string) ([]map[string]any, error) {
	raw, err := readNodeSpec(path)
	if err != nil {
		return nil, err
	}
	decoded,err:=contractschema.DecodeJSON(raw);if err!=nil{return nil,err}
	spec,ok:=decoded.(map[string]any);if !ok{return nil,fmt.Errorf("spec must be an object")}
	list, ok := spec["fields"].([]any)
	if !ok {
		return nil, fmt.Errorf("spec fields shape")
	}
	var out []map[string]any
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("spec field shape")
		}
		composition,_:=spec["composition"].(map[string]any)
		side,_:=m["side"].(string);name,_:=m["name"].(string)
		fields,_:=composition[side+"s"].(map[string]any);semantic,_:=fields[name].(map[string]any)
		if inner,ok:=semantic["inner"].(map[string]any);ok{m["inner"]=inner}
		if rules,ok:=semantic["requirements"].(map[string]any);ok{m["requirements"]=rules}
		out = append(out, m)
	}
	return out, nil
}

func topo(nodes []processNode, edges []processEdge) ([][]string, error) {
	indeg := map[string]int{}
	succ := map[string][]string{}
	known := map[string]bool{}
	for _, n := range nodes {
		indeg[n.key] = 0
		known[n.key] = true
	}
	for _, e := range edges {
		if e.fromNode == "" || e.toNode == "" {
			return nil, fmt.Errorf("edge endpoint blank")
		}
		if !known[e.fromNode] || !known[e.toNode] {
			return nil, fmt.Errorf("edge on unknown node")
		}
		succ[e.fromNode] = append(succ[e.fromNode], e.toNode)
		indeg[e.toNode]++
	}
	var levels [][]string
	done := 0
	for {
		var ready []string
		for k, d := range indeg {
			if d == 0 {
				ready = append(ready, k)
			}
		}
		if len(ready) == 0 {
			break
		}
		sort.Strings(ready)
		levels = append(levels, ready)
		for _, k := range ready {
			indeg[k] = -1
			done++
			for _, m := range succ[k] {
				indeg[m]--
			}
		}
	}
	if done != len(nodes) {
		return nil, fmt.Errorf("process cycle")
	}
	return levels, nil
}

func runNode(nodes []processNode, edges []processEdge, specs map[string][]map[string]any, snap map[string]map[string]any, key string, bv boundView, handle egress.InvocationHandle) (map[string]any, error) {
	return runNodeWith(nodes, edges, specs, snap, key, bv, handle, nil)
}

// runNodeWith is runNode plus a loop-state overlay: the loop executor passes
// the previous round's state without inventing process edges.
var nodeInvocationObservations sync.Map
func runNodeWith(nodes []processNode, edges []processEdge, specs map[string][]map[string]any, snap map[string]map[string]any, key string, bv boundView, handle egress.InvocationHandle, overlay map[string]any) (map[string]any, error) {
 timing:=startNodeTiming(key);defer timing.finish()
	var node *processNode
	for i := range nodes {
		if nodes[i].key == key {
			node = &nodes[i]
		}
	}
	if node == nil {
		return nil, fmt.Errorf("unknown node")
	}
	fields := specs[key]
	inputs := map[string]any{}
	for name, value := range node.fills {
		inputs[name] = value
	}
	for _, e := range edges {
		if e.toNode != key {
			continue
		}
		up, ok := snap[e.fromNode]
		if !ok {
			return nil, fmt.Errorf("missing upstream %s", e.fromNode)
		}
		value, ok := up[e.fromField]
		if !ok {
			continue // Required consumers refuse at their input validation below.
		}
		if e.pill != nil {
			address, err := parsePillAddress(strMap(e.pill, "address"))
			if err != nil { return nil, err }
			slot, err := parsePillSlot(strMap(e.pill, "slot"))
			if err != nil { return nil, err }
			if address.Kind != "composition" || slot.Binding != "early" || address.Port != e.fromField {
				return nil, fmt.Errorf("composition pill edge contract")
			}
			var producer *processNode
			for i := range nodes { if nodes[i].key == e.fromNode { producer = &nodes[i]; break } }
			if producer == nil || producer.node != address.Target.Ref || producer.digest != address.Target.RevisionDigest {
				return nil, fmt.Errorf("composition pill supplier pin")
			}
			if _, object := value.(map[string]any); object {
				selected, present, err := SelectSteps(value, pillCompileSteps(address.FieldPath))
				if err != nil || !present { return nil, fmt.Errorf("composition pill field %s absent", address.FieldPath) }
				value = selected
			} else if address.FieldPath != address.Port {
				return nil, fmt.Errorf("composition pill field %s does not address scalar port %s", address.FieldPath, address.Port)
			}
			if err := checkPillType(slot.Type.Name, value); err != nil { return nil, err }
			if err := checkPillBounds(slot.Bounds, value); err != nil { return nil, err }
			digest, ok := canonDigest(normJSON(value), "praxis.pill.value.v1")
			if !ok { return nil, fmt.Errorf("composition pill value undigestible") }
			pillAppendRecord(key, map[string]any{"node":key,"field":e.toField,"ref":address.Target.Ref,"port":address.Port,"field_path":address.FieldPath,"kind":address.Kind,"check":"ok","value_digest":digest})
		}
		inputs[e.toField] = value
	}
	for name, value := range overlay {
		inputs[name] = value
	}
	// Intrinsic runtime inputs are injected from the execution identity, never
	// from fills, edges, overlays or caller input. Compile validation already
	// refuses conflicts; this is the execution-time defense.
	for name, value := range node.runtimeFills {
		if _, exists := inputs[name]; exists {
			return nil, fmt.Errorf("runtime input %s conflicts with a bound input", name)
		}
		inputs[name] = value
	}
	// Late-bound pills resolve here, before any other input validation:
	// each blank binds strictly inside the execution scope (invocation
	// inputs, fills, completed outputs) against its declared late slot,
	// pin, and receipt. Unresolvable in scope is refusal, always.
	if err := resolvePillInputs(key, nodes, inputs, snap); err != nil {
		return nil, err
	}
	for _, f := range fields {
		name, _ := f["name"].(string)
		side, _ := f["side"].(string)
		required, _ := f["required"].(bool)
		if side != "input" {
			continue
		}
		value, present := inputs[name]
		if !present {
			if required {
				return nil, fmt.Errorf("input %s unfilled", name)
			}
			if err:=checkRequirementValue(f,nil);err!=nil{return nil,err}
			continue
		}
		if err := checkValue(f, value, bv.forKey(key, "input", name)); err != nil {
			return nil, fmt.Errorf("input %s: %v", name, err)
		}
	}
	// Each invocation owns its input tree, including nested maps and arrays.
	inputs, cloneErr := detachValues(inputs)
	if cloneErr != nil { return nil, fmt.Errorf("inputs: %w", cloneErr) }
	entry := dispatch[node.reference()]
    configuration:=entry.config;if node.configuration!=nil{configuration=*node.configuration}
    for _,field:=range fields{if side,_:=field["side"].(string);side=="config"{
        value,err:=valuecontract.Decode([]byte(configuration));if err!=nil{return nil,fmt.Errorf("configuration is invalid")}
        name,_:=field["name"].(string);if err:=checkValue(field,value,bv.forKey(key,"config",name));err!=nil{return nil,fmt.Errorf("configuration %s: %w",name,err)}
    }}
 configurationPointer:=node.configuration
 configurationDigest,digestErr:=egress.ConfigurationSelectionDigest(configurationPointer);if digestErr!=nil{return nil,digestErr}
 inputDigest,digestErr:=egress.ObservationDigest(normJSON(inputs));if digestErr!=nil{return nil,digestErr}
 observationKey:=key;if node.receiptKey!=""{observationKey=node.receiptKey}
 invocationEgressRunID:=""
 if entry.scoped!=nil||entry.scopedConfig!=nil {provider,ok:=handle.(interface{InvocationRunID()string});if !ok{return nil,fmt.Errorf("execution selection boundary absent")};invocationEgressRunID=provider.InvocationRunID();if invocationEgressRunID==""{return nil,fmt.Errorf("execution selection identity absent")}}
 invocationObservation:=map[string]any{"configuration_digest":configurationDigest,"input_digest":inputDigest,"origin":"process_observed"}
 if invocationEgressRunID!=""{invocationObservation["egress_run_id"]=invocationEgressRunID}
 nodeInvocationObservations.Store(observationKey,invocationObservation)
 if entry.scoped!=nil||entry.scopedConfig!=nil {binder,ok:=handle.(interface{BindInputs(string)error});if !ok{return nil,fmt.Errorf("input selection boundary absent")};if err:=binder.BindInputs(inputDigest);err!=nil{return nil,err}}
	timing.phase("execute")
	var out map[string]any
	var err error
	if entry.infer!=nil {
		if handle==nil {return nil,fmt.Errorf("inference invocation absent")}
		defer handle.Cancel()
		raw,readErr:=readNodeSpec(entry.spec);if readErr!=nil{return nil,readErr}
		contract,contractErr:=egress.InferContractFromNodeSpec(raw);if contractErr!=nil{return nil,contractErr}
		client,contractErr:=egress.NewContractInferClient(inferenceClient{handle},contract);if contractErr!=nil{return nil,contractErr}
		out,err=entry.infer(client,inputs)
		if violation:=client.Err();violation!=nil {err=violation}
		if err==nil {
			if pinErr:=egress.RequireCorpusPin(inputs,out); pinErr!=nil {err=pinErr}
		}
	} else if entry.scopedConfig!=nil {
		if handle==nil {return nil,fmt.Errorf("scoped invocation absent")}
		defer handle.Cancel()
		configuration:=entry.config
		if node.configuration!=nil {configuration=*node.configuration}
		out,err=entry.scopedConfig(operationClient{handle},configuration,inputs)
	} else if entry.scoped!=nil {
		if handle==nil {return nil,fmt.Errorf("scoped invocation absent")}
		defer handle.Cancel()
		out,err=entry.scoped(operationClient{handle},inputs)
	} else if entry.harness!=nil {
		var receipt map[string]any
		out,receipt,err=entry.harness(entry.spec, inputs)
		if receipt!=nil {receiptKey:=key;if node.receiptKey!=""{receiptKey=node.receiptKey};driverReceipts.Store(receiptKey, receipt)}
	} else if entry.runConfig!=nil {
		configuration:=entry.config
 if node.configuration!=nil { configuration=*node.configuration }
 out,err=entry.runConfig(configuration,inputs)
	} else {out,err=entry.run(inputs)}
	if err != nil {
		return nil, err
	}
	timing.phase("outputs")
	out, err = preserveValues(out)
	if err != nil { return nil, fmt.Errorf("outputs: %w", err) }
	if entry.spec!="" && node.digest!="" {rawOutcomeSpec,outcomeReadErr:=readNodeSpec(entry.spec);if outcomeReadErr!=nil{return nil,outcomeReadErr}
	var outcomeSpec map[string]json.RawMessage;if outcomeReadErr=json.Unmarshal(rawOutcomeSpec,&outcomeSpec);outcomeReadErr!=nil{return nil,outcomeReadErr}
 var outcomeBindings []egress.OutcomeBinding;if value:=outcomeSpec["outcome_bindings"];value!=nil{if outcomeReadErr=json.Unmarshal(value,&outcomeBindings);outcomeReadErr!=nil{return nil,outcomeReadErr}}
 if outcomeReadErr=egress.ValidateOutcomeBindings(outcomeBindings,inputs,out);outcomeReadErr!=nil{return nil,outcomeReadErr}}

	for _, f := range fields {
		name, _ := f["name"].(string)
		side, _ := f["side"].(string)
		if side != "output" {
			continue
		}
		value, present := out[name]
		if !present {
			if required,_:=f["required"].(bool);required {return nil, fmt.Errorf("output %s missing", name)}
			if err:=checkRequirementValue(f,nil);err!=nil{return nil,err}
			continue
		}
		if err := checkValue(f, value, bv.forKey(key, "output", name)); err != nil {
			return nil, fmt.Errorf("output %s: %v", name, err)
		}
	}
 outputDigest,digestErr:=egress.ObservationDigest(normJSON(out));if digestErr!=nil{return nil,digestErr}
 settledObservation:=map[string]any{"configuration_digest":configurationDigest,"input_digest":inputDigest,"output_digest":outputDigest,"origin":"process_observed"}
 if invocationEgressRunID!=""{settledObservation["egress_run_id"]=invocationEgressRunID}
 nodeInvocationObservations.Store(observationKey,settledObservation)
	return detachValues(out)
}

// Trigger gate: when the print pinned identities in
// worker/triggers.json, invocation requires -trigger ID and a valid
// ed25519 signature over the canonical invocation document
// (composition, effective fills, trigger id). No pinned identities:
// the invocation stays open, but naming -trigger then refuses rather
// than running ungated. The verified document and its digest return
// for the evidence store and replay detection.
type invocation struct {
	digest string
	raw    string
}

func checkTriggerGate(comp string, dep *deployment, nodes []processNode, f cliFlags) (*invocation, error) {
	raw, err := os.ReadFile(filepath.Join("worker", "triggers.json"))
	if os.IsNotExist(err) {
		if f.trigger != "" || f.triggerSigEnv != "" {
			return nil, fmt.Errorf("trigger gate: this print has no trigger identities")
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trigger gate: triggers.json unreadable")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("trigger gate: %v", err)
	}
	idents, _ := doc["identities"].(map[string]any)
	if f.trigger == "" {
		return nil, fmt.Errorf("trigger gate: invocation needs -trigger ID")
	}
	entry, ok := idents[f.trigger].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("trigger gate: unknown trigger %q", f.trigger)
	}
	kind, _ := entry["kind"].(string)
	pubHex, _ := entry["pubkey"].(string)
	if kind != "ed25519" {
		return nil, fmt.Errorf("trigger gate: trigger %q kind %q", f.trigger, kind)
	}
	pub, err := hex.DecodeString(pubHex)
	if err != nil || len(pub) != 32 {
		return nil, fmt.Errorf("trigger gate: trigger %q pubkey", f.trigger)
	}
	if f.triggerSigEnv == "" {
		return nil, fmt.Errorf("trigger gate: needs -trigger-sig-env NAME")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv(f.triggerSigEnv)))
	if err != nil {
		return nil, fmt.Errorf("trigger gate: signature: %v", err)
	}
	fills := map[string]any{}
	for _, n := range nodes {
		if len(n.fills) == 0 {
			continue
		}
		inner := map[string]any{}
		for k, v := range n.fills {
			inner[k] = v
		}
		fills[n.key] = inner
	}
	document:=map[string]any{"composition": comp, "fills": fills, "trigger": f.trigger}
	if f.eventID!="" {document["event_id"]=f.eventID;document["diagnostics"]=f.diagnostics}
	if f.tool!="" {document["tool"]=f.tool};if f.flow!="" {document["flow"]=f.flow}
	msg, ok := canonCompact(document)
	if !ok {
		return nil, fmt.Errorf("trigger gate: invocation undigestible")
	}
	inv := &invocation{raw: msg}
	inv.digest, ok = canonDigest(map[string]any{"invocation": msg}, "praxis.invocation.v1")
	if !ok {
		return nil, fmt.Errorf("trigger gate: invocation undigestible")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(msg), sig) {
		return nil, fmt.Errorf("trigger gate: signature invalid")
	}
	// Replay detection only after the signature verifies: an
	// unauthenticated caller learns nothing about run history.
	if dep != nil && dep.evidence != "" && f.eventID=="" {
		if _, err := os.Stat(filepath.Join(dep.evidence, "s0", inv.digest+".json")); err == nil {
			return nil, fmt.Errorf("trigger replay")
		}
	}
	return inv, nil
}

func checkRequirementValue(f map[string]any,value any)error{
	if raw,ok:=f["requirements"];ok {
	 b,err:=json.Marshal(raw);if err!=nil{return err};var rules requirements.Requirements
	 if err=json.Unmarshal(b,&rules);err!=nil{return err}
	 if err=requirements.RequirementsExecutable(&rules);err!=nil{return err}
	 inner,_:=f["inner"].(map[string]any)
	 checks,err:=requirements.EvaluateRequirements(&rules,value,len(inner)>0);if err!=nil{return err}
	 for _,check:=range checks{if check.State=="violated"{return fmt.Errorf("requirement %s: %s",check.ID,check.Reason)}}
	}
	return nil
}
func checkValue(f map[string]any, value any, entries []boundEntry) error {
	tn, _ := f["type"].(map[string]any)
	if err:=valuecontract.CheckIntegers(tn,value);err!=nil{return err}
	if _,err:=valuecontract.Exact(value);err!=nil{return err}
	if err:=checkRequirementValue(f,value);err!=nil{return err}
	schema,err:=contractschema.ParseMap(tn,contractschema.ModeStrict)
	if err!=nil{return err}
	if err:=schema.FirstError(value,"$");err!=nil{return err}
	if inner,ok:=f["inner"].(map[string]any);ok&&len(inner)>0{
	 text,ok:=value.(string);if !ok{return fmt.Errorf("inner JSON requires string transport")}
	 decoded,err:=contractschema.DecodeJSON([]byte(text));if err!=nil{return err}
	 if err:=valuecontract.CheckIntegers(inner,decoded);err!=nil{return fmt.Errorf("inner JSON: %v",err)}
	 if _,err:=valuecontract.Exact(decoded);err!=nil{return fmt.Errorf("inner JSON: %v",err)}
	 schema,err:=contractschema.ParseMap(inner,contractschema.ModeStrict);if err!=nil{return err}
	 if err:=schema.FirstError(decoded,"$");err!=nil{return fmt.Errorf("inner JSON: %v",err)}
	}
	if err := checkJSONNumbers(value); err != nil { return err }
	for _, e := range entries {
		if err := checkBound(e, value); err != nil {
			return err
		}
	}
	return nil
}

// NodeSpec-cited cost ceilings: the printed spec cites deployment bounds
// by key plus digest, so the ceiling is pinned at print time — a
// deployment that loosens a cited bound drifts and refuses. Kinds
// are closed: count (string length or numeric min/max), range
// (numeric lo/hi), set (string membership).

type boundEntry struct {
	kind    string
	subject string
	params  map[string]any
}

type boundView map[string][]boundEntry

func (bv boundView) forKey(node, side, name string) []boundEntry {
	return bv[node+"\x00"+side+"\x00"+name]
}

func listOf(m map[string]any, key string) []any {
	raw, _ := m[key].([]any)
	return raw
}

func toFloat(v any) (float64, bool) {
	n,err:=valuecontract.Normalize(v);if err!=nil{return 0,false}
	f, ok := n.(float64)
	return f, ok
}

func numParam(params map[string]any, name string) (float64, bool) {
	return toFloat(params[name])
}

// loadSealedBounds reads worker/bounds.json: the print-pinned bound set
// that ships inside the sealed composition. Absent means the print pinned
// none (legacy and source layouts fall back to deployment bounds alone).
func loadSealedBounds() ([]map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join("worker", "bounds.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sealed bounds unreadable")
	}
	var doc map[string]any
	if err := decodeProcessJSON(raw, &doc); err != nil {
		return nil, fmt.Errorf("worker/bounds.json: %v", err)
	}
	out := []map[string]any{}
	for _, item := range listOf(doc, "bounds") {
		e, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sealed bounds shape")
		}
		key, _ := e["key"].(string)
		kind, _ := e["kind"].(string)
		subject, _ := e["subject"].(string)
		params, _ := e["params"].(map[string]any)
		if key == "" || subject == "" || params == nil {
			return nil, fmt.Errorf("sealed bound shape")
		}
		if err := valuecontract.ValidateBound(kind, params); err != nil {
			return nil, err
		}
		switch kind {
		case "count", "range", "set":
		default:
			return nil, fmt.Errorf("sealed bound kind %q", kind)
		}
		out = append(out, map[string]any{"key": key, "kind": kind, "subject": subject, "params": params})
	}
	return out, nil
}

// resolveBounds builds the field bound view from the sealed set merged
// with deployment bounds. SEALED-EXACT: a sealed entry is authoritative
// for a cited ref, so a deployment entry for the same key is ignored and
// can never loosen or replace the ceiling pinned at print; deployment
// bounds contribute only refs the sealed set does not cover. Enforcement
// is unconditional: every cited ref is resolved here, and a cited ref
// with no entry at all refuses, whether or not a deployment exists.
func resolveBounds(sealed []map[string]any, dep *deployment, nodes []processNode, specs map[string][]map[string]any) (boundView, error) {
	out := boundView{}
	byRef := map[string]map[string]any{}
 sealedKeys:=map[string]bool{}
 for _,e:=range sealed {
  key,_:=e["key"].(string);digest,ok:=canonDigest(e,"praxis.bound.v1")
  if key=="" || !ok {return nil,fmt.Errorf("sealed bound key or digest")}
  ref:=key+"\x00"+digest
  if _,dup:=byRef[ref];dup {return nil,fmt.Errorf("sealed bound %s duplicated",key)}
  byRef[ref]=e;sealedKeys[key]=true
 }
 if dep!=nil {for _,e:=range dep.bounds {
  key,_:=e["key"].(string);if sealedKeys[key] {continue}
  digest,ok:=canonDigest(e,"praxis.bound.v1");if key=="" || !ok {return nil,fmt.Errorf("deployment bound key or digest")}
  byRef[key+"\x00"+digest]=e
 }}
	for _, n := range nodes {
		targetFields:=[]valuecontract.FieldTarget{}
		for _,f:=range specs[n.key]{side,_:=f["side"].(string);name,_:=f["name"].(string);targetFields=append(targetFields,valuecontract.FieldTarget{Side:side,Name:name})}
		for _, f := range specs[n.key] {
			name, _ := f["name"].(string)
			side, _ := f["side"].(string)
			for _, item := range listOf(f, "bounds") {
				ref, ok := item.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("node %s: bound ref shape", n.key)
				}
				key, _ := ref["key"].(string)
				want, _ := ref["digest"].(string)
				entry, ok := byRef[key+"\x00"+want]
				if !ok {
                    if sealedKeys[key] {return nil,fmt.Errorf("node %s: bound %s drift",n.key,key)}
					return nil, fmt.Errorf("node %s: bound %s unresolved", n.key, key)
				}
				got, ok := canonDigest(entry, "praxis.bound.v1")
				if !ok || got != want {
					return nil, fmt.Errorf("node %s: bound %s drift", n.key, key)
				}
				kind, _ := entry["kind"].(string)
				subj, _ := entry["subject"].(string)
				params, _ := entry["params"].(map[string]any)
				if err:=valuecontract.ValidateBound(kind,params);err!=nil{return nil,err}
				target,err:=valuecontract.ResolveBound(subj,valuecontract.FieldTarget{Side:side,Name:name},targetFields);if err!=nil{return nil,err}
				if target.Scope=="metadata" {
					description,_:=f["description"].(map[string]any)
					if unknown,_:=description["unknown"].(bool);unknown{return nil,fmt.Errorf("metadata bound requires known description")}
					if err:=valuecontract.CheckMetadata(kind,target,params,description[target.Metadata]);err!=nil{return nil,err}
					continue
				}
				if target.Scope=="structural"{continue}
				k := n.key + "\x00" + target.Side + "\x00" + target.Field
				out[k] = append(out[k], boundEntry{kind: kind, subject: subj, params: params})
			}
		}
	}
	return out, nil
}

func checkBound(e boundEntry, value any) error {
	measurement:="value";if strings.HasPrefix(e.subject,"length("){measurement="length"}
	return valuecontract.CheckBound(e.kind,measurement,e.params,value)
}

func checkMinMax(n float64, params map[string]any, what string) error {
	if mn, ok := numParam(params, "min"); ok && n < mn {
		return fmt.Errorf("bound_exceeded: %s %v below min", what, n)
	}
	if mx, ok := numParam(params, "max"); ok && n > mx {
		return fmt.Errorf("bound_exceeded: %s %v above max", what, n)
	}
	return nil
}

// checkRunBounds applies the run-level ceilings (attempts, duration) after
// execution. Sealed entries are consulted first, so a print-pinned
// run-level bound is authoritative over a deployment one for the same
// subject; deployment entries cover subjects the sealed set does not.
func checkRunBounds(sealed []map[string]any, dep *deployment, elapsed time.Duration) error {
	find := func(subject string) map[string]any {
		for _, e := range sealed {
			if s, _ := e["subject"].(string); s == subject {
				return e
			}
		}
		if dep != nil {
			return dep.boundBySubject(subject)
		}
		return nil
	}
	if e := find("attempts"); e != nil {
		params, _ := e["params"].(map[string]any)
		if err := checkMinMax(1, params, "attempts"); err != nil {
			return err
		}
	}
	if e := find("duration"); e != nil {
		params, _ := e["params"].(map[string]any)
		if mx, ok := numParam(params, "max"); ok && elapsed.Seconds() > mx {
			return fmt.Errorf("bound_exceeded: duration %v above max", elapsed.Round(time.Second))
		}
	}
	return nil
}

func verifySeal(processPath string) (string, []string, error) {
	comp, dirs, err := compositionDigest(processPath)
	if err != nil {
		return "", nil, err
	}
	sealRaw, err := os.ReadFile("seal.json")
	if err != nil {
		return "", nil, fmt.Errorf("seal missing")
	}
	var env struct {
		Composition string
		Declaration string
		Deployment  string
		Acceptance  string
		Kind        string
		Signature   string
	}
	if err := json.Unmarshal(sealRaw, &env); err != nil {
		return "", nil, fmt.Errorf("seal envelope: %v", err)
	}
	pubRaw, err := os.ReadFile("forge_key.pub")
	if err != nil {
		return "", nil, fmt.Errorf("forge key missing")
	}
	pub, err := hex.DecodeString(strings.TrimSpace(string(pubRaw)))
	if err != nil || len(pub) != 32 {
		return "", nil, fmt.Errorf("forge key shape")
	}
	if env.Kind != "praxis.seal.v1" {
		return "", nil, fmt.Errorf("seal kind %q", env.Kind)
	}
	if env.Composition != comp {
		return "", nil, fmt.Errorf("composition drift")
	}
	deployHex := ""
	if draw, err := os.ReadFile("deployment.json"); err == nil {
		sum := sha256.Sum256(draw)
		deployHex = hex.EncodeToString(sum[:])
	} else if !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("deployment unreadable")
	}
	if env.Deployment != "" && env.Deployment != deployHex {
		return "", nil, fmt.Errorf("deployment drift")
	}
	// The declaration digest is part of the signed envelope: the worker
	// recomputes it from the shipped declaration.json and refuses both
	// directions of presence mismatch (a file present when the envelope
	// says empty, or vice versa) before trusting any signature.
	declHex := ""
	if draw, err := os.ReadFile("declaration.json"); err == nil {
		sum := sha256.Sum256(draw)
		declHex = hex.EncodeToString(sum[:])
	} else if !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("declaration unreadable")
	}
	if env.Declaration == "" && declHex != "" {
		return "", nil, fmt.Errorf("declaration present but not sealed")
	}
	if env.Declaration != "" && declHex == "" {
		return "", nil, fmt.Errorf("sealed declaration missing")
	}
	if env.Declaration != declHex {
		return "", nil, fmt.Errorf("declaration drift")
	}
	acceptanceHex := ""
	if draw, err := os.ReadFile("portable_acceptance.json"); err == nil {
		sum := sha256.Sum256(draw)
		acceptanceHex = hex.EncodeToString(sum[:])
	} else if !os.IsNotExist(err) {
		return "", nil, fmt.Errorf("portable acceptance unreadable")
	}
	if env.Acceptance == "" && acceptanceHex != "" {
		return "", nil, fmt.Errorf("portable acceptance present but not sealed")
	}
	if env.Acceptance != "" && acceptanceHex == "" {
		return "", nil, fmt.Errorf("sealed portable acceptance missing")
	}
	if env.Acceptance != acceptanceHex {
		return "", nil, fmt.Errorf("portable acceptance drift")
	}
	sealMessage := map[string]any{"composition": env.Composition, "declaration": env.Declaration, "deployment": env.Deployment, "kind": env.Kind}
	if env.Acceptance != "" {
		sealMessage["acceptance"] = env.Acceptance
	}
	msg, ok := canonCompact(sealMessage)
	if !ok {
		return "", nil, fmt.Errorf("seal undigestible")
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return "", nil, fmt.Errorf("seal signature: %v", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(msg), sig) {
		return "", nil, fmt.Errorf("seal signature invalid")
	}
	return comp, dirs, nil
}

func compositionDigest(processPath string) (string, []string, error) {
	wfRaw, err := os.ReadFile(processPath)
	if err != nil {
		return "", nil, err
	}
	files := map[string]map[string]string{}
	dirs := map[string]bool{}
	err = filepath.Walk("nodes", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel := filepath.ToSlash(path)
		if strings.Contains("/"+rel+"/", "/tokens/") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		trimmed := strings.TrimPrefix(rel, "nodes/")
		leafDir := ""
		if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
			leafDir = trimmed[:idx]
		}
		dirs[leafDir] = true
		if files[leafDir] == nil {
			files[leafDir] = map[string]string{}
		}
		files[leafDir][trimmed] = bytesDigest(raw, "praxis.composition.leaf.v1")
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	dirList := make([]string, 0, len(dirs))
	grouped := make([]string, 0, len(dirs))
	for d := range dirs {
		grouped = append(grouped, d)
		if d == "" {
			continue
		}
		dirList = append(dirList, d)
	}
	sort.Strings(dirList)
	sort.Strings(grouped)
	nodes := make([]any, 0, len(grouped))
	for _, d := range grouped {
		fm := map[string]any{}
		for k, v := range files[d] {
			fm[k] = v
		}
		nodes = append(nodes, map[string]any{"dir": d, "files": fm})
	}
	workerFiles := map[string]any{}
	for _,tree:=range []string{"worker","internal"} {
	err = filepath.Walk(tree, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		workerFiles[filepath.ToSlash(path)] = bytesDigest(raw, "praxis.composition.leaf.v1")
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	}
	modRaw, err := os.ReadFile("go.mod")
	if err != nil {
		return "", nil, err
	}
	hostFiles := map[string]any{}
	err = filepath.Walk(filepath.Join("host", "bin"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hostFiles[filepath.ToSlash(path)] = bytesDigest(raw, "praxis.composition.leaf.v1")
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	value := map[string]any{
		"nodes":    nodes,
		"gomod":    bytesDigest(modRaw, "praxis.composition.leaf.v1"),
		"host":     hostFiles,
		"worker":   workerFiles,
		"process": bytesDigest(wfRaw, "praxis.composition.leaf.v1"),
	}
	digest, ok := canonDigest(value, "praxis.composition.v1")
	if !ok {
		return "", nil, fmt.Errorf("composition undigestible")
	}
	return digest, dirList, nil
}

func bytesDigest(raw []byte, purpose string) string {
	h := sha256.New()
	h.Write([]byte(purpose))
	h.Write([]byte{0x00})
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))
}

func canonDigest(value any, purpose string) (string, bool) {
	encoded, ok := canonCompact(map[string]any{"purpose": purpose, "value": value})
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(sum[:]), true
}

func canonCompact(v any) (string, bool) {
	var b strings.Builder
	if !canonValue(&b, v) {
		return "", false
	}
	return b.String(), true
}

// exactNumberToken reduces one JSON number token to its exact canonical form
// without rounding: sign, significant digits and a decimal exponent with
// trailing zeros removed. Integer values render as plain digits; everything
// else renders as d[.ddd]e[exp]. The portable verifier applies the identical
// reduction to the wire token, so both producers agree byte for byte.
func exactNumberToken(text string) (string, bool) {
	i := 0
	negative := false
	if i < len(text) && text[i] == '-' {
		negative = true
		i++
	}
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	intDigits := text[start:i]
	if intDigits == "" || (len(intDigits) > 1 && intDigits[0] == '0') {
		return "", false
	}
	fracDigits := ""
	if i < len(text) && text[i] == '.' {
		i++
		fs := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		fracDigits = text[fs:i]
		if fracDigits == "" {
			return "", false
		}
	}
	exponent := new(big.Int)
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		sign := 1
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			if text[i] == '-' {
				sign = -1
			}
			i++
		}
		es := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if es == i {
			return "", false
		}
		value, ok := new(big.Int).SetString(text[es:i],10)
		if !ok {
			return "", false
		}
		exponent.Set(value)
		if sign < 0 { exponent.Neg(exponent) }
	}
	if i != len(text) {
		return "", false
	}
	digits := intDigits + fracDigits
	exp10 := new(big.Int).Sub(exponent,big.NewInt(int64(len(fracDigits))))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", true
	}
	for len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exp10.Add(exp10,big.NewInt(1))
	}
	sign := ""
	if negative {
		sign = "-"
	}
	if exp10.Sign() == 0 {
		return sign + digits, true
	}
	scientific := new(big.Int).Add(exp10,big.NewInt(int64(len(digits)-1)))
	if len(digits) == 1 {
		return sign + digits + "e" + scientific.String(), true
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + scientific.String(), true
}

// canonicalExactNumber reports the exact canonical form of a token that must
// not pass through float64, and false when the established canonical float
// form applies (any integer value within the exact float range, whatever its
// token shape). The reduction is a pure function of the token, so the runtime
// producer and the portable verifier agree byte for byte.
func canonicalExactNumber(text string) (string, bool) {
	canonical, ok := exactNumberToken(text)
	if !ok {
		return "", false
	}
	if canonicalIntegerWithinExactFloat(canonical) {
		return "", false
	}
	return canonical, true
}

// canonicalIntegerWithinExactFloat reports whether an exact canonical token is
// an integer value within ±(2^53-1), which keeps the established float form.
func canonicalIntegerWithinExactFloat(canonical string) bool {
	body := strings.TrimPrefix(canonical, "-")
	mantissa := body
	exponent := 0
	if index := strings.IndexAny(body, "eE"); index >= 0 {
		mantissa = body[:index]
		value, err := strconv.Atoi(body[index+1:])
		if err != nil {
			return false
		}
		exponent = value
	}
	digits := mantissa
	if index := strings.Index(mantissa, "."); index >= 0 {
		digits = mantissa[:index] + mantissa[index+1:]
		exponent -= len(mantissa) - index - 1
	}
	if exponent < 0 {
		return false
	}
	digitCount := len(digits) + exponent
	if digitCount <= 0 {
		return false
	}
	if digitCount < 16 {
		return true
	}
	if digitCount > 16 {
		return false
	}
	return digits+strings.Repeat("0", exponent) <= "9007199254740991"
}

func canonValue(b *strings.Builder, v any) bool {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		canonString(b, t)
	case int:
		text,ok:=exactNumberToken(strconv.FormatInt(int64(t),10));if !ok{return false};b.WriteString(text)
	case float64:
		// Digest the value the encoder will actually write, so the portable
		// verifier recomputes the same canonical form from the wire token.
		raw,err:=json.Marshal(t);if err!=nil{return false}
		if text,exact:=canonicalExactNumber(string(raw));exact{b.WriteString(text);break}
		canonFloat(b,t)
	case json.Number:
		if text,exact:=canonicalExactNumber(string(t));exact{b.WriteString(text);break}
		token,ok:=exactNumberToken(string(t));if !ok{return false};n,err:=valuecontract.Number(token);if err!=nil{return false};canonFloat(b,n)
	case []any:
		b.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if !canonValue(b, e) {
				return false
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			canonString(b, k)
			b.WriteByte(':')
			if !canonValue(b, t[k]) {
				return false
			}
		}
		b.WriteByte('}')
	default:
		return false
	}
	return true
}

func canonString(b *strings.Builder, s string) {
	const digits = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		case '\b':
			b.WriteString("\\b")
		case '\f':
			b.WriteString("\\f")
		default:
			if r < 0x20 || r > 0x7e {
				rr := r
				if rr > 0xffff {
					rr -= 0x10000
					canonU16(b, digits, rune(0xd800+(rr>>10)))
					canonU16(b, digits, rune(0xdc00+(rr&0x3ff)))
				} else {
					canonU16(b, digits, rr)
				}
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

func canonU16(b *strings.Builder, digits string, r rune) {
	b.WriteString("\\u")
	b.WriteByte(digits[(r>>12)&0xf])
	b.WriteByte(digits[(r>>8)&0xf])
	b.WriteByte(digits[(r>>4)&0xf])
	b.WriteByte(digits[r&0xf])
}

func canonFloat(b *strings.Builder, f float64) {
	switch {
	case math.IsInf(f, 1):
		b.WriteString("Infinity")
		return
	case math.IsInf(f, -1):
		b.WriteString("-Infinity")
		return
	case math.IsNaN(f):
		b.WriteString("NaN")
		return
	}
	if f == 0 {
		b.WriteString(strconv.FormatFloat(f, 'f', -1, 64) + ".0")
		return
	}
	abs := f
	if abs < 0 {
		abs = -abs
	}
	if abs >= 1e-4 && abs < 1e16 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		b.WriteString(s)
		return
	}
	b.WriteString(strconv.FormatFloat(f, 'e', -1, 64))
}

// Sealed credentials: node tokens persist only as AES-256-GCM
// ciphertext sharded across the node dirs. The data key derives from
// the customer deployment key plus the composition digest, so only
// the live worker with the exact node set recovers anything.

type shard struct {
	Version int
	Composition string
	Data        string
	Index       int
	Node        string
	Of          int
}

func sealRun(comp string, dirs []string, tokenEnvs []string, deployEnv string) error {
	if deployEnv == "" {
		return fmt.Errorf("seal needs -deployment-key-env NAME")
	}
	deployKey, err := deployKeyFromEnv(deployEnv)
	if err != nil {
		return err
	}
	sealed := map[string]string{}
	for _, te := range tokenEnvs {
		key, name, ok := strings.Cut(te, "=")
		if !ok || key == "" {
			return fmt.Errorf("seal needs key=NAME token refs, got %q", te)
		}
		if strings.ContainsAny(key,"/\\\x00") || key=="." || key==".." {return fmt.Errorf("seal: unsafe node key")}
		value := os.Getenv(name)
		if value == "" {
			return fmt.Errorf("seal: env %s empty for node %s", name, key)
		}
		sealed[key] = value
	}
	if len(sealed) == 0 {
		return fmt.Errorf("seal: no node tokens given")
	}
	if len(dirs) == 0 {
		return fmt.Errorf("seal: needs at least one node dir")
	}
	// A one-directory process stores one authenticated ciphertext shard.
	// Larger groups still distribute one shard per directory.
	n := len(dirs)
	dataKey := deriveDataKey(deployKey, comp)
	names := make([]string, 0, len(sealed))
	for key, token := range sealed {
		blob, err := sealToken(deriveDataKey(dataKey, "node:"+key), token)
		if err != nil {
			return err
		}
		chunks := splitBlob(blob, n)
		for i, chunk := range chunks {
			dir := dirs[i%len(dirs)]
			sh := shard{Version:2, Composition: comp, Data: chunk, Index: i, Node: key, Of: n}
			raw, err := json.Marshal(sh)
			if err != nil {
				return err
			}
			shardDir := filepath.Join("nodes", dir, "tokens")
			if err := os.MkdirAll(shardDir, 0o755); err != nil {
				return err
			}
			path := filepath.Join(shardDir, fmt.Sprintf("%s.shard%dof%d.json", key, i, n))
			if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
				return err
			}
		}
		names = append(names, key)
	}
	sort.Strings(names)
	out, err := json.Marshal(map[string]any{"composition": comp, "ok": true, "sealed": names})
	if err != nil {
		return err
	}
	printDevelopmentOutcome(out)
	return nil
}

func loadSealedTokensFor(comp, deployEnv, onlyNode string) (map[string]string, error) {
	out := map[string]string{}
	groups := map[string][]shard{}
	found := false
	err := filepath.Walk("nodes", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "tokens" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var sh shard
		if err := json.Unmarshal(raw, &sh); err != nil {
			return fmt.Errorf("shard %s: %v", path, err)
		}
		if onlyNode!="" && sh.Node!=onlyNode {return nil}
		groups[sh.Node] = append(groups[sh.Node], sh)
		found = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !found {
		return out, nil
	}
	if deployEnv == "" {
		return nil, fmt.Errorf("sealed composition needs -deployment-key-env NAME")
	}
	deployKey, err := deployKeyFromEnv(deployEnv)
	if err != nil {
		return nil, err
	}
	dataKey := deriveDataKey(deployKey, comp)
	for node, shards := range groups {
		sort.Slice(shards, func(i, j int) bool { return shards[i].Index < shards[j].Index })
		want := shards[0].Of
		if shards[0].Version!=2 {return nil,fmt.Errorf("scoped credentials require node-keyed v2 shards; reseal in customer custody")}
		if len(shards) != want {
			return nil, fmt.Errorf("node %s: %d of %d shards", node, len(shards), want)
		}
		var blob strings.Builder
		for i, sh := range shards {
			if sh.Composition != comp {
				return nil, fmt.Errorf("node %s: composition drift", node)
			}
			if sh.Index != i || sh.Of != want || sh.Version!=2 {
				return nil, fmt.Errorf("node %s: shard set broken", node)
			}
			blob.WriteString(sh.Data)
		}
		token, err := openToken(deriveDataKey(dataKey,"node:"+node), blob.String())
		if err != nil {
			return nil, fmt.Errorf("node %s: %v", node, err)
		}
		out[node] = token
	}
	return out, nil
}

func evidenceGet(comp, deployEnv, key string) error {
	dep, err := loadDeployment()
	if err != nil {
		return err
	}
	if dep == nil || dep.evidence == "" {
		return fmt.Errorf("no evidence store")
	}
	if deployEnv == "" {
		deployEnv = dep.dataKey
	}
	deployKey, err := deployKeyFromEnv(deployEnv)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dep.evidence)
	if err != nil {
		return err
	}
	shards := map[int]shard{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dep.evidence, e.Name(), key+".json"))
		if err != nil {
			continue
		}
		var sh shard
		if err := json.Unmarshal(raw, &sh); err != nil {
			return fmt.Errorf("evidence shard corrupt")
		}
		shards[sh.Index] = sh
	}
	if len(shards) == 0 {
		return fmt.Errorf("evidence %s unseen", key)
	}
	of := 0
	for _, sh := range shards {
		if sh.Composition != comp {
			return fmt.Errorf("evidence composition drift")
		}
		of = sh.Of
	}
	if len(shards) != of {
		return fmt.Errorf("evidence %d of %d shards", len(shards), of)
	}
	var blob strings.Builder
	for i := 0; i < of; i++ {
		sh, ok := shards[i]
		if !ok || sh.Of != of {
			return fmt.Errorf("evidence shard set broken")
		}
		blob.WriteString(sh.Data)
	}
	plain, err := openToken(deriveDataKey(deployKey, comp), blob.String())
	if err != nil {
		return fmt.Errorf("evidence %v", err)
	}
	fmt.Println(plain)
	return nil
}

// persistEvidence files one run's records into the customer-owned
// evidence store: the verified invocation document (when gated) and
// the result JSON, each AES-GCM-encrypted under the deployment data
// key and split across the spread of shard directories. Every record
// is digest-keyed; nothing here is ambient state.
func persistEvidence(dep *deployment, comp, deployEnv string, inv *invocation, result []byte) error {
	deployKey, err := deployKeyFromEnv(deployEnv)
	if err != nil {
		return err
	}
	dataKey := deriveDataKey(deployKey, comp)
	sum := sha256.Sum256(result)
	rkey := hex.EncodeToString(sum[:])
	units := []struct {
		key  string
		data []byte
	}{{rkey, result}}
	if inv != nil {
		units = append(units, struct {
			key  string
			data []byte
		}{inv.digest, []byte(inv.raw)})
	}
	for _, u := range units {
		blob, err := sealToken(dataKey, string(u.data))
		if err != nil {
			return err
		}
		chunks := splitBlob(blob, dep.spread)
		for i, chunk := range chunks {
			dir := filepath.Join(dep.evidence, fmt.Sprintf("s%d", i))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			sh := shard{Composition: comp, Data: chunk, Index: i, Node: u.key, Of: dep.spread}
			raw, err := json.Marshal(sh)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, u.key+".json"), append(raw, '\n'), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func deployKeyFromEnv(name string) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	key, err := hex.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("deployment key in %s must be 64 hex chars", name)
	}
	return key, nil
}

// deployment is the customer-owned deployment.json: absent means direct
// runs; present it carries field fills, the data-key env ref, evidence
// policy, and bounds. Legacy token shares refuse. Print may pin its
// digest in the seal; drift then refuses.
type deployment struct {
	fills    map[string]map[string]any
	dataKey  string
	evidence string
	spread   int
	bounds   []map[string]any
	// pillSlots maps "node.field" to a pill.slot.v1 document declaring the
	// late slot for a pill blank; pillPins maps target refs to expected
	// revision digests; extensions opens named tool-input slots the harness
	// may pass that the compiled inputs never saw, each with its endpoint
	// and accepted type. All three are optional and refusal-checked.
	pillSlots  map[string]map[string]any
	pillPins   map[string]string
	extensions []pillExtensionPoint
}

func (d *deployment) boundBySubject(subject string) map[string]any {
	for _, e := range d.bounds {
		if s, _ := e["subject"].(string); s == subject {
			return e
		}
	}
	return nil
}

func loadDeployment() (*deployment, error) {
	raw, err := os.ReadFile("deployment.json")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deployment unreadable")
	}
	var doc map[string]any
	if err := decodeProcessJSON(raw, &doc); err != nil {
		return nil, fmt.Errorf("deployment.json: %v", err)
	}
	if _, legacy := doc["shares"]; legacy {
		return nil, fmt.Errorf("deployment.json shares are not supported: credential grants are the only run-time credential path")
	}
	dep := &deployment{fills: map[string]map[string]any{}, spread: 2}
	dep.dataKey, _ = doc["data_key"].(string)
	if m, ok := doc["evidence"].(map[string]any); ok {
		dir, _ := m["dir"].(string)
		if dir == "" {
			return nil, fmt.Errorf("deployment.json evidence shape")
		}
		if dir == "nodes" || dir == "worker" || strings.HasPrefix(dir, "nodes/") || strings.HasPrefix(dir, "worker/") {
			return nil, fmt.Errorf("deployment.json evidence inside covered tree")
		}
		dep.evidence = dir
	}
	if v, present := doc["spread"]; present {
		if err:=valuecontract.CheckIntegers(map[string]any{"type":"integer"},v);err!=nil{return nil,err}
		n, ok := toFloat(v)
		if !ok || n < 2 || n > float64(^uint(0)>>1) {
			return nil, fmt.Errorf("deployment.json spread below 2")
		}
		dep.spread = int(n)
	}
	for _, item := range listOf(doc, "bounds") {
		e, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("deployment bounds shape")
		}
		key, _ := e["key"].(string)
		kind, _ := e["kind"].(string)
		subject, _ := e["subject"].(string)
		params, _ := e["params"].(map[string]any)
		if key == "" || subject == "" || params == nil {
			return nil, fmt.Errorf("deployment bound shape")
		}
		if err:=valuecontract.ValidateBound(kind,params);err!=nil{return nil,err}
		switch kind {
		case "count", "range", "set":
		default:
			return nil, fmt.Errorf("deployment bound kind %q", kind)
		}
		dep.bounds = append(dep.bounds, map[string]any{"key": key, "kind": kind, "subject": subject, "params": params})
	}
	if fills, ok := doc["fills"].(map[string]any); ok {
		for key, value := range fills {
			node, field, ok := strings.Cut(key, ".")
			if !ok || node == "" || field == "" {
				return nil, fmt.Errorf("deployment.json fill %q: want node.field", key)
			}
			if dep.fills[node] == nil {
				dep.fills[node] = map[string]any{}
			}
			dep.fills[node][field] = value
		}
	}
	// Late pill declarations: slots per node field, pins per target ref,
	// and the named tool-input extension points. Shapes refuse here; graph
	// references (known nodes, input fields, edge collisions) refuse in
	// initPillScope once the printed graph is loaded.
	if raw, present := doc["pill_slots"]; present {
		slots, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("deployment.json pill_slots shape")
		}
		dep.pillSlots = map[string]map[string]any{}
		for key, value := range slots {
			node, field, ok := strings.Cut(key, ".")
			if !ok || node == "" || field == "" {
				return nil, fmt.Errorf("deployment.json pill slot %q: want node.field", key)
			}
			slot, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("deployment.json pill slot %q shape", key)
			}
			if _, err := parsePillSlot(slot); err != nil {
				return nil, fmt.Errorf("deployment.json pill slot %s: %v", key, err)
			}
			dep.pillSlots[key] = slot
		}
	}
	if raw, present := doc["pill_pins"]; present {
		pins, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("deployment.json pill_pins shape")
		}
		dep.pillPins = map[string]string{}
		for ref, value := range pins {
			digest, ok := value.(string)
			if !ok || !pillDigestShape(digest) {
				return nil, fmt.Errorf("deployment.json pill pin %q unverifiable", ref)
			}
			dep.pillPins[ref] = digest
		}
	}
	if raw, present := doc["extension_points"]; present {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("deployment.json extension_points shape")
		}
		seen := map[string]bool{}
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("deployment.json extension point shape")
			}
			name, _ := entry["name"].(string)
			node, _ := entry["node"].(string)
			field, _ := entry["field"].(string)
			typ, ok := entry["type"].(map[string]any)
			if name == "" || node == "" || field == "" || !ok || len(typ) == 0 {
				return nil, fmt.Errorf("deployment.json extension point needs name, node, field, and type")
			}
			for member := range entry {
				switch member {
				case "name", "node", "field", "type":
				default:
					return nil, fmt.Errorf("deployment.json extension point member %q", member)
				}
			}
			if seen[name] {
				return nil, fmt.Errorf("deployment.json extension point %q duplicated", name)
			}
			seen[name] = true
			dep.extensions = append(dep.extensions, pillExtensionPoint{name: name, node: node, field: field, typ: typ})
		}
	}
	return dep, nil
}

func deriveDataKey(deployKey []byte, comp string) []byte {
	mac := hmac.New(sha256.New, deployKey)
	mac.Write([]byte("praxis.composition.data_key.v1"))
	mac.Write([]byte(comp))
	return mac.Sum(nil)
}

func sealToken(dataKey []byte, token string) (string, error) {
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(token), nil)
	blob := make([]byte, 0, len(nonce)+len(ct))
	blob = append(blob, nonce...)
	blob = append(blob, ct...)
	return base64.StdEncoding.EncodeToString(blob), nil
}

func openToken(dataKey []byte, blob string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(blob)
	if err != nil || len(raw) < 13 {
		return "", fmt.Errorf("shard blob corrupt")
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, raw[:12], raw[12:], nil)
	if err != nil {
		return "", fmt.Errorf("open failed")
	}
	return string(plain), nil
}

func splitBlob(blob string, n int) []string {
	out := make([]string, 0, n)
	base, rem := len(blob)/n, len(blob)%n
	at := 0
	for i := 0; i < n; i++ {
		size := base
		if i < rem {
			size++
		}
		out = append(out, blob[at:at+size])
		at += size
	}
	return out
}

// normJSON converts Go ints to float64 so receipt digests recompute
// identically from JSON-decoded bytes (JSON has one number form).
func normJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normJSON(e)
		}
		return out
	case int:
		return float64(t)
	case int8:
		return float64(t)
	case int16:
		return float64(t)
	case int32:
		return float64(t)
	case int64:
		// Exact integers and json.Number tokens are never coerced through
		// float64: 2^53+ magnitudes would round before the receipt is written.
		return t
	case uint:
		return float64(t)
	case uint8:
		return float64(t)
	case uint16:
		return float64(t)
	case uint32:
		return float64(t)
	case uint64:
		return t
	case float32:
		return float64(t)
	default:
		return v
	}
}

var nodeTimings sync.Map
var nodeTimingEnabled bool
var nodeTimingOrigin time.Time
func beginNodeTimings(enabled bool) {nodeTimingEnabled=enabled;nodeTimingOrigin=time.Now();nodeTimings=sync.Map{}}
func timingInteger(n int64)json.Number{return json.Number(strconv.FormatInt(n,10))}
type nodeTiming struct {key,name string;start,phaseStart int64;phases []any}
func startNodeTiming(key string)*nodeTiming {
 if !nodeTimingEnabled{return nil};offset:=time.Since(nodeTimingOrigin).Nanoseconds()
 return &nodeTiming{key:key,name:"inputs",start:offset,phaseStart:offset,phases:[]any{}}
}
func(t *nodeTiming)closePhase(end int64) {
 t.phases=append(t.phases,map[string]any{"name":t.name,"started_offset_ns":timingInteger(t.phaseStart),"finished_offset_ns":timingInteger(end),"duration_ns":timingInteger(end-t.phaseStart)})
}
func(t *nodeTiming)phase(name string) {if t==nil{return};end:=time.Since(nodeTimingOrigin).Nanoseconds();t.closePhase(end);t.phaseStart=end;t.name=name}
func(t *nodeTiming)finish() {
 if t==nil{return};end:=time.Since(nodeTimingOrigin).Nanoseconds();t.closePhase(end)
 nodeTimings.Store(t.key,map[string]any{"schema":"praxis.node_timing.v1","clock":"process_monotonic","run_started_at":nodeTimingOrigin.UTC().Format(time.RFC3339Nano),"started_offset_ns":timingInteger(t.start),"finished_offset_ns":timingInteger(end),"duration_ns":timingInteger(end-t.start),"phases":t.phases})
}

func beginEvent(comp string,nodes []processNode,boundary *processRun,f cliFlags,keyEnv string)(*executionjournal.Journal,*executionjournal.Outcome,error){
 if boundary!=nil && (boundary.recovery!=nil || boundary.progression) && f.eventID=="" {return nil,nil,fmt.Errorf("recovery or progression requires event identity and execution-owned state directory")}
 if f.eventID=="" {return nil,nil,nil}
 if boundary==nil {return nil,nil,fmt.Errorf("events require a compiled Tool or Flow")}
 key,err:=hex.DecodeString(strings.TrimSpace(os.Getenv(keyEnv)))
 if err!=nil || len(key)!=32 {return nil,nil,fmt.Errorf("event state requires an execution-owned 32-byte deployment key")}
 fills:=map[string]any{};for _,n:=range nodes {fills[n.key]=n.fills}
 
 scope,ok:=canonDigest(map[string]any{"process":boundary.ref["name"],"tool":f.tool,"flow":f.flow,"trigger":f.trigger},"praxis.event_scope.v2")
 if !ok {return nil,nil,fmt.Errorf("event scope undigestible")}
 policy:=map[string]any{"fills":fills,"diagnostics":f.diagnostics,"credential_mode":f.credentialMode,"composition":comp,"process":boundary.ref}
 if boundary.invocationAuthorityBinding!=nil {policy["invocation_authority"]=boundary.invocationAuthorityBinding}
 for name,path:=range map[string]string{"credential_grants":f.credentialGrants,"egress_bindings":f.egressBindings}{if path!="" {raw,err:=os.ReadFile(path);if err!=nil{return nil,nil,err};sum:=sha256.Sum256(raw);policy[name]=hex.EncodeToString(sum[:])}}
 payload,ok:=canonDigest(policy,"praxis.event_payload.v1")
 if !ok {return nil,nil,fmt.Errorf("event payload undigestible")}
 if boundary.recovery!=nil || boundary.progression {return executionjournal.Open(f.eventState,key,scope,f.eventID,payload)}
 return executionjournal.Open(f.eventState,key,scope,f.eventID,payload)
}
func receiptRequirements(key string,nodes []processNode,edges []processEdge,specs map[string][]map[string]any,results map[string]map[string]any) []any {
 var records []any
 inputs:=map[string]any{}
 for _,n:=range nodes{if n.key==key{for k,v:=range n.fills{inputs[k]=v}}}
 for _,e:=range edges{if e.toNode==key{if v,ok:=results[e.fromNode][e.fromField];ok{inputs[e.toField]=v}}}
 for _,f:=range specs[key]{raw,ok:=f["requirements"];if !ok{continue};name,_:=f["name"].(string);side,_:=f["side"].(string)
  value:=inputs[name];if side=="output"{value=results[key][name]}
  b,err:=json.Marshal(raw);if err!=nil{continue};var rules requirements.Requirements;if json.Unmarshal(b,&rules)!=nil{continue}
  inner,_:=f["inner"].(map[string]any);checks,err:=requirements.EvaluateRequirements(&rules,value,len(inner)>0)
  if err!=nil{records=append(records,map[string]any{"field":name,"side":side,"state":"evaluation_failed"});continue}
  for _,check:=range checks{records=append(records,map[string]any{"id":check.ID,"node":key,"field":name,"side":side,"state":check.State,"reason":check.Reason,"policy_digest":requirements.RequirementsBehaviorDigest(&rules)})}
 };return records
}
type reportedFailure struct{}
func (reportedFailure) Error() string {return "process execution failed"}
// Generic process data uses exact JSON transport: numbers stay exact tokens,
// never float64. Float64 conversion is reserved for explicitly typed
// consumers such as bounds and decision numerics.
func decodeProcessJSON(raw []byte,out *map[string]any) error {
 v,err:=valuecontract.Decode(raw);if err!=nil{return err}
 if v==nil {*out=nil;return nil}
 obj,ok:=v.(map[string]any);if !ok{return fmt.Errorf("JSON object required")}
 *out=obj;return nil
}
func checkJSONNumbers(value any) error {
 _,err:=valuecontract.Exact(value);return err
}
// JSON round-trip rejects cycles/non-JSON values and detaches mutable
// containers without routing numbers through float64.
func detachValues(values map[string]any)(map[string]any,error) {
 if values==nil{return nil,nil}
 v,err:=valuecontract.Exact(values);if err!=nil{return nil,err}
 return v.(map[string]any),nil
}
func preserveValues(values map[string]any)(map[string]any,error) {
 if values==nil{return nil,nil};raw,err:=json.Marshal(values);if err!=nil{return nil,err}
 v,err:=valuecontract.Decode(raw);if err!=nil{return nil,err};return v.(map[string]any),nil
}
type processFailure struct {
 process any
 source any
 node string
 composition string
 cause error
}
func (e *processFailure) Error() string {return fmt.Sprintf("node %s: %v",e.node,e.cause)}
func (e *processFailure) Unwrap() error {if e==nil{return nil};return e.cause}
type processRun struct {
 recovery *runtimeRecoveryPolicy
 progression bool
 allowedBindings map[string]bool
 ref map[string]any
 sources map[string]any
 outputs []any
 exposure map[string]any
 invocationAuthorityBinding map[string]any
 invInputDigest string
 invInputs map[string]any
}
func bindProcess(processDoc map[string]any,nodes []processNode,f cliFlags,dep *deployment,edges []processEdge)(*processRun,error) {
 doc,exists:=processDoc["process"]
 if !exists {
  if f.input!="" || f.tool!="" || f.flow!="" {return nil,fmt.Errorf("process flags require compiled process")}
  return nil,nil
 }
 p,ok:=doc.(map[string]any);if !ok || p["schema"]!="aeltum.compiled_process.v2" {return nil,fmt.Errorf("invalid process boundary")}
 if (f.tool=="")== (f.flow=="") {return nil,fmt.Errorf("select exactly one -tool or -flow")}
 role,name,list:="tool",f.tool,"tools";if f.flow!="" {role,name,list="flow",f.flow,"flows"}
 var exposure map[string]any
 for _,item:=range listOf(p,list) {candidate,_:=item.(map[string]any);if candidate["name"]==name {exposure=candidate}}
 if exposure==nil {return nil,fmt.Errorf("unknown process exposure")}
	result:=&processRun{ref:strMap(p,"ref"),sources:strMap(p,"sources"),outputs:listOf(p,"outputs"),exposure:map[string]any{"role":role,"name":name}}
	if _,present:=processDoc["progression"];present {result.progression=true}
	if value,present:=strMap(p,"policy")["recovery"];present {raw,err:=json.Marshal(value);if err!=nil{return nil,err};var policy runtimeRecoveryPolicy;decoder:=json.NewDecoder(bytes.NewReader(raw));decoder.DisallowUnknownFields();if err:=decoder.Decode(&policy);err!=nil{return nil,err};result.recovery=&policy}
 result.exposure["trigger_id"]=f.trigger
 if role=="flow" {result.exposure["trigger_kind"]=exposure["trigger_kind"]}
 supplied:=map[string]any{}
 if f.input!="" {
  raw,err:=readInputBytes(f.input);if err!=nil {return nil,err}
  hash:=sha256.Sum256(raw);result.invInputDigest="sha256:"+hex.EncodeToString(hash[:])
  if err:=decodeProcessJSON(raw,&supplied);err!=nil {return nil,err};if supplied==nil {return nil,fmt.Errorf("process input must be a JSON object")}
 }
 expected:=map[string]bool{}
 for _,entry:=range listOf(p,"inputs") {
  port,ok:=entry.(map[string]any);if !ok {return nil,fmt.Errorf("invalid process port")}
  key,_:=port["name"].(string);expected[key]=true
  value,present:=supplied[key]
  if !present {if required,_:=port["required"].(bool);required {return nil,fmt.Errorf("process input %s missing",key)};continue}
  if err:=checkValue(port,value,nil);err!=nil {return nil,fmt.Errorf("process input %s: %v",key,err)}
  endpoint:=strMap(port,"endpoint");node,_:=endpoint["node"].(string);field,_:=endpoint["field"].(string)
  found:=false
  for i:=range nodes {if nodes[i].key==node {found=true;if _,conflict:=nodes[i].fills[field];conflict {return nil,fmt.Errorf("process input %s conflicts with deployment fill",key)};nodes[i].fills[field]=value}}
  if !found {return nil,fmt.Errorf("process input endpoint missing")}
 }
 for name:=range supplied {if !expected[name] {
  // Open tool-input slots: the harness may pass never-seen fields, but
  // only through declared extension points. Each carries its endpoint and
  // accepted type; the value is type-checked and lands in the endpoint
  // fill. Anything undeclared is refused exactly as before, and the worker
  // never calls back anywhere to learn more: the declaration is complete.
  ext:=findPillExtension(dep,name)
  if ext==nil {return nil,fmt.Errorf("unknown process input %s",name)}
  if err:=checkValue(map[string]any{"name":name,"type":ext.typ},supplied[name],nil);err!=nil {return nil,fmt.Errorf("process input %s: %v",name,err)}
  placed:=false
  for i:=range nodes {if nodes[i].key==ext.node {placed=true
   if _,conflict:=nodes[i].fills[ext.field];conflict {return nil,fmt.Errorf("process input %s conflicts with deployment fill",name)}
   for _,e:=range edges {if e.toNode==ext.node&&e.toField==ext.field {return nil,fmt.Errorf("process input %s conflicts with an edge",name)}}
   nodes[i].fills[ext.field]=supplied[name]}}
  if !placed {return nil,fmt.Errorf("process input %s endpoint missing",name)}
 }}
 result.invInputs=map[string]any{}
 for name,value:=range supplied {result.invInputs[name]=value}
 return result,nil
}
func (p *processRun) project(results map[string]map[string]any)(map[string]any,error) {
 out:=map[string]any{}
 for _,entry:=range p.outputs {
  port,_:=entry.(map[string]any);name,_:=port["name"].(string)
  endpoint:=strMap(port,"endpoint");node,_:=endpoint["node"].(string);field,_:=endpoint["field"].(string)
  value,ok:=results[node][field]
  if !ok {if required,_:=port["required"].(bool);required {return nil,fmt.Errorf("process output %s missing",name)};continue}
  if err:=checkValue(port,value,nil);err!=nil {return nil,fmt.Errorf("process output %s: %v",name,err)}
  out[name]=value
 }
 return out,nil
}

func readInferencePolicies()(map[string]egress.InferencePolicy,error){raw,err:=os.ReadFile("worker/inference.json");if os.IsNotExist(err){return map[string]egress.InferencePolicy{},nil};if err!=nil{return nil,err};return egress.ParseInferencePolicies(raw)}
func describeAuthority(comp string,document map[string]any,nodes []processNode) error {
 inferencePolicies,err:=readInferencePolicies();if err!=nil{return err}
 descriptions:=[]any{}
 for _,node:=range nodes {
  entry,ok:=dispatch[node.reference()];if !ok{return fmt.Errorf("unknown node")}
  if entry.infer!=nil {p,ok:=inferencePolicies[node.key];if !ok{return fmt.Errorf("inference policy absent")};d,err:=p.Digest();if err!=nil{return err};descriptions=append(descriptions,map[string]any{"node_key":node.key,"execution":"trusted_inference","policy_digest":d,"policy":p});continue}
  if entry.harness!=nil {
   doc,err:=specDocument(entry.spec);if err!=nil{return err}
   handoff:=specDimension(doc,"handoff")
   name,_:=handoff["harness"].(string);ref,_:=handoff["contract"].(map[string]any);digest,_:=ref["digest"].(string)
   ownership,_:=handoff["ownership"].(string);interactivity,_:=handoff["interactivity"].(string);channel,_:=handoff["credential_channel"].(string)
   descriptions=append(descriptions,map[string]any{"node_key":node.key,"execution":"harness_dispatch","harness":name,"contract_digest":digest,"ownership":ownership,"interactivity":interactivity,"credential_channel":channel})
   continue
  }
  if entry.scoped==nil && entry.scopedConfig==nil {descriptions=append(descriptions,map[string]any{"node_key":node.key,"execution":"trusted_unscoped"});continue}
  raw,err:=readNodeSpec(entry.spec);if err!=nil{return err}
  var spec struct{Dimensions []struct{Name string;Contents json.RawMessage}}
  if err:=json.Unmarshal(raw,&spec);err!=nil{return err}
  var policy egress.Policy;for _,d:=range spec.Dimensions {if d.Name=="egress"{policy,err=egress.Parse(d.Contents);if err!=nil{return err}}}
  digest,err:=policy.Digest();if err!=nil{return err}
  descriptions=append(descriptions,map[string]any{"node_key":node.key,"execution":"trusted_shared_memory","policy_digest":digest,"policy":policy})
 }
 var processRef any;if process,ok:=document["process"].(map[string]any);ok{processRef=process["ref"]}
 raw,err:=json.Marshal(map[string]any{"process_digest":comp,"process_ref":processRef,"nodes":descriptions});if err!=nil{return err};fmt.Println(string(raw));return nil
}

// operationClient deliberately hides handle cancellation and resolver
// authority from trusted Node adapters. It does not isolate hostile Go memory.
type operationClient struct { handle egress.InvocationHandle }
func(c operationClient)Accounting()egress.Accounting{return egress.ReceiptAccounting(c.handle.Receipt())}
func(c operationClient)Receipt()egress.Receipt{return c.handle.Receipt()}
type inferenceClient struct { handle egress.InvocationHandle }
type multiOperationHandle struct { handles map[string]egress.InvocationHandle }
func (m *multiOperationHandle) InvocationRunID() string {
 run:="";for _,h:=range m.handles{provider,ok:=h.(interface{InvocationRunID()string});if !ok{return ""};id:=provider.InvocationRunID();if id==""||(run!=""&&run!=id){return ""};run=id};return run
}
func (m *multiOperationHandle) BindInputs(digest string) error {
 for _,h:=range m.handles {binder,ok:=h.(interface{BindInputs(string)error});if !ok{return fmt.Errorf("input selection boundary absent")};if err:=binder.BindInputs(digest);err!=nil{return err}}
 return nil
}
func (m *multiOperationHandle) Invoke(ctx context.Context, raw []byte)(egress.Result,error){
 var request egress.OperationRequest
 dec:=json.NewDecoder(bytes.NewReader(raw));dec.DisallowUnknownFields()
 if err:=dec.Decode(&request);err!=nil{return egress.Result{},fmt.Errorf("invalid operation request")}
 var trailing any;if dec.Decode(&trailing)!=io.EOF{return egress.Result{},fmt.Errorf("trailing operation request")}
 if request.Operation=="" {return egress.Result{},fmt.Errorf("operation selector required")}
 handle:=m.handles[request.Operation];if handle==nil{return egress.Result{},fmt.Errorf("undeclared operation")}
 return handle.Invoke(ctx,raw)
}
func (m *multiOperationHandle) InvokeStream(ctx context.Context, raw []byte, onEvent egress.EventStreamHandler)(egress.Result,error){
 var request egress.OperationRequest
 dec:=json.NewDecoder(bytes.NewReader(raw));dec.DisallowUnknownFields()
 if err:=dec.Decode(&request);err!=nil{return egress.Result{},fmt.Errorf("invalid request envelope")}
 var trailing any;if dec.Decode(&trailing)!=io.EOF{return egress.Result{},fmt.Errorf("trailing request envelope")}
 if request.Operation=="" {return egress.Result{},fmt.Errorf("operation selector required")}
 handle:=m.handles[request.Operation];if handle==nil{return egress.Result{},fmt.Errorf("undeclared operation")}
 streaming,ok:=handle.(egress.EventStreamInvocationHandle);if !ok{return egress.Result{},fmt.Errorf("event streaming unavailable")}
 return streaming.InvokeStream(ctx,raw,onEvent)
}
func (m *multiOperationHandle) Cancel() error {
 names:=make([]string,0,len(m.handles));for name:=range m.handles{names=append(names,name)};sort.Strings(names)
 var first error;for _,name:=range names{if err:=m.handles[name].Cancel();err!=nil&&first==nil{first=err}};return first
}
func (m *multiOperationHandle) Receipt() egress.Receipt {return egress.Receipt{Outcome:"multiple_operations",Transmission:"not_sent"}}
func (m *multiOperationHandle) Receipts() []egress.Receipt {
 names:=make([]string,0,len(m.handles));for name:=range m.handles{names=append(names,name)};sort.Strings(names)
 out:=make([]egress.Receipt,0,len(names));for _,name:=range names{out=append(out,m.handles[name].Receipt())};return out
}
func(c inferenceClient)Infer(ctx context.Context,request egress.InferRequest)(egress.InferResponse,error){
 raw,err:=json.Marshal(request);if err!=nil{return egress.InferResponse{},err}
 result,err:=c.handle.Invoke(ctx,raw);if err!=nil{return egress.InferResponse{},err}
 // Decode the provider response as a generic document, then overlay the
 // attempt truth from the invocation receipt: a substitute that served is
 // labeled fallback with the winning binding, never plain success. The
 // type-specific receipt stays the authority; this only keeps the response
 // shape uniform across transports.
 dec:=json.NewDecoder(bytes.NewReader(result.Response));dec.UseNumber()
 var envelope map[string]any
 if err:=dec.Decode(&envelope);err!=nil{return egress.InferResponse{},err}
 winner:="";fallback:=false;succeeded:=false
 for i,attempt:=range result.Receipt.InferenceAttempts {
  if attempt.Error!="" {continue}
  winner=attempt.Binding
  fallback=i>0
  succeeded=true
  break
 }
 if succeeded {
  if _,present:=envelope["winning_binding"];!present{envelope["winning_binding"]=winner}
  if _,present:=envelope["fallback"];!present{envelope["fallback"]=fallback}
 }
 merged,err:=json.Marshal(envelope);if err!=nil{return egress.InferResponse{},err}
 var response egress.InferResponse
 if err:=json.Unmarshal(merged,&response);err!=nil{return egress.InferResponse{},err}
 return response,nil
}
func (c operationClient) Do(ctx context.Context, request egress.OperationRequest) (egress.OperationResponse,error) {
 raw,err:=json.Marshal(request);if err!=nil{return egress.OperationResponse{},err}
 result,callErr:=c.handle.Invoke(ctx,raw)
 var response egress.OperationResponse
 if len(result.Response)==0{if callErr!=nil{return response,callErr};return response,fmt.Errorf("operation response absent")}
 if err:=json.Unmarshal(result.Response,&response);err!=nil{if callErr!=nil{return egress.OperationResponse{},fmt.Errorf("%w; operation response decoding failed: %v",callErr,err)};return egress.OperationResponse{},err}
 return response,callErr
}
func (c operationClient) Stream(ctx context.Context, request egress.OperationRequest, onEvent egress.EventStreamHandler) (egress.OperationResponse,error) {
 if onEvent==nil{return egress.OperationResponse{},fmt.Errorf("event stream handler required")}
	streaming,ok:=c.handle.(egress.EventStreamInvocationHandle);if !ok{return egress.OperationResponse{},fmt.Errorf("event streaming unavailable")}
	raw,err:=json.Marshal(request);if err!=nil{return egress.OperationResponse{},err}
	result,streamErr:=streaming.InvokeStream(ctx,raw,onEvent)
	var response egress.OperationResponse
	if len(result.Response)==0{return response,streamErr}
	if err:=json.Unmarshal(result.Response,&response);err!=nil{return response,err}
	return response,streamErr
}

type customerSecrets struct { composition, deploymentKey string }
func (s customerSecrets) ResolveSecret(ctx context.Context, source string) ([]byte,error) {
 if err:=ctx.Err();err!=nil{return nil,err}
 if name,ok:=strings.CutPrefix(source,"env:");ok && name!="" {value:=os.Getenv(name);if value=="" {return nil,fmt.Errorf("credential unavailable")};return []byte(value),nil}
 if node,ok:=strings.CutPrefix(source,"sealed:");ok && node!="" {
  tokens,err:=loadSealedTokensFor(s.composition,s.deploymentKey,node);if err!=nil{return nil,err};return []byte(tokens[node]),nil
 }
 return nil,fmt.Errorf("unsupported customer custody reference")
}

func prepareEgress(comp string, boundary *processRun, nodes []processNode, f cliFlags, deployEnv string) (map[string]egress.InvocationHandle,error) {
 return prepareSelectedEgress(comp,boundary,nodes,nil,f,deployEnv)
}
// Selection is Process-owned static activation membership, not a caller DAG.
// Inactive bindings remain data and cannot demand credentials or open handles.
func prepareSelectedEgress(comp string,boundary *processRun,all []processNode,selected map[string]bool,f cliFlags,deployEnv string)(map[string]egress.InvocationHandle,error){
 nodes:=[]processNode{};known:=map[string]bool{};knownScoped:=map[string]bool{}
 for _,node:=range all{known[node.key]=true;entry:=dispatch[node.reference()];knownScoped[node.key]=entry.scoped!=nil||entry.scopedConfig!=nil||entry.infer!=nil;if selected==nil||selected[node.key]{nodes=append(nodes,node)}}
 if selected!=nil{for key:=range selected{if !known[key]{return nil,fmt.Errorf("egress activation selection names unknown Node")}}}
 handles:=map[string]egress.InvocationHandle{}
 browserAdapter:=egress.NewNativeBrowser()
 scoped:=false;for _,node:=range nodes {if dispatch[node.reference()].scoped!=nil || dispatch[node.reference()].scopedConfig!=nil || dispatch[node.reference()].infer!=nil {scoped=true}}
 if !scoped && f.credentialGrants=="" && f.egressBindings=="" && !f.publicReadPipe && !f.providerHTTPSPipe {return handles,nil}
 good:=false
 var err error
 var publicReadPipe *egress.PublicReadPipeClient
 var providerHTTPSPipe *egress.ProviderHTTPSPipeClient
 if f.providerHTTPSPipe {
  if boundary==nil || f.eventID=="" || f.providerHTTPSPackageDigest=="" {return nil,fmt.Errorf("provider HTTPS pipe requires exact Process, EventID and package identity")}
  providerHTTPSPipe,err=egress.OpenProviderHTTPSPipeClient();if err!=nil{return nil,fmt.Errorf("selected provider HTTPS pipe unavailable")}
 }
 defer func(){if !good {cancelEgressHandles(handles);if publicReadPipe!=nil{_ = publicReadPipe.Close()}}}()
 if f.publicReadPipe {
  if boundary==nil || f.eventID=="" || f.publicReadPackageDigest=="" {return nil,fmt.Errorf("public-read pipe requires exact Process, EventID, and package identity")}
  publicReadPipe,err=egress.OpenPublicReadPipeClient();if err!=nil{return nil,fmt.Errorf("selected public-read pipe unavailable: %v",err)}
 }
 mode:=f.credentialMode;if mode=="" {mode=egress.Interactive}
 if mode!=egress.Interactive && mode!=egress.Triggered {return nil,fmt.Errorf("unsupported credential mode")}
 if f.trigger!="" && mode!=egress.Triggered {return nil,fmt.Errorf("trigger execution requires triggered credential mode")}
 random:=make([]byte,32);if _,err:=rand.Read(random);err!=nil{return nil,err};runID:=hex.EncodeToString(random)
 var grants []egress.CredentialGrant
 bindings:=map[string]egress.Binding{}
 if f.egressBindings!="" {
  raw,err:=os.ReadFile(f.egressBindings);if err!=nil||len(raw)>65536{return nil,fmt.Errorf("egress bindings unavailable or too large")}
  if _,err:=contractschema.DecodeJSON(raw);err!=nil{return nil,fmt.Errorf("invalid egress bindings")}
  dec:=json.NewDecoder(bytes.NewReader(raw));dec.DisallowUnknownFields();if err:=dec.Decode(&bindings);err!=nil{return nil,fmt.Errorf("invalid egress bindings")}
  for _,b:=range bindings {if b.CredentialRef!="" {return nil,fmt.Errorf("network bindings cannot supply credential authority")}}
 }
 if f.credentialGrants!="" {
  raw,err:=os.ReadFile(f.credentialGrants);if err!=nil{return nil,fmt.Errorf("credential grants unavailable")}
  if len(raw)>65536 {return nil,fmt.Errorf("credential grants too large")}
  if _,err:=contractschema.DecodeJSON(raw);err!=nil{return nil,fmt.Errorf("invalid credential grants")}
  dec:=json.NewDecoder(bytes.NewReader(raw));dec.DisallowUnknownFields();if err:=dec.Decode(&grants);err!=nil{return nil,fmt.Errorf("invalid credential grants")}
  var trailing any;if dec.Decode(&trailing)!=io.EOF{return nil,fmt.Errorf("trailing credential grants")}
 }
 for i:=range grants {
  g:=&grants[i]
  if g.Mode!=mode || g.RunID!="" || g.ProcessDigest!=comp {return nil,fmt.Errorf("credential grant mode or revision mismatch")}
  if mode==egress.Interactive {if !strings.HasPrefix(g.Source,"env:"){return nil,fmt.Errorf("interactive credential must be supplied for this invocation")};g.RunID=runID}
  if node,ok:=strings.CutPrefix(g.Source,"sealed:");ok && node!=g.NodeKey{return nil,fmt.Errorf("sealed credential reference must match node")}
 }
 for _,g:=range grants{if !knownScoped[g.NodeKey]{return nil,fmt.Errorf("credential grant names an unscoped or unknown node")}}
 for key:=range bindings{base:=key;if !knownScoped[base] && boundary!=nil && boundary.allowedBindings[key]{delete(bindings,key);continue};if !knownScoped[base]{match:="";for k:=range knownScoped{if knownScoped[k]&&strings.HasPrefix(key,k+"/")&&len(k)>len(match){match=k}};if match==""{return nil,fmt.Errorf("network binding names an unscoped or unknown node")};base=match};if selected!=nil&&!selected[base]{delete(bindings,key)}}
 if selected!=nil{active:=[]egress.CredentialGrant{};for _,g:=range grants{if selected[g.NodeKey]{active=append(active,g)}};grants=active}
 provider,err:=egress.NewGrantedCredentials(mode,grants,customerSecrets{comp,deployEnv});if err!=nil{return nil,err}
 used:=map[int]bool{}
 usedBindings:=map[string]bool{}
 inferencePolicies:=map[string]egress.InferencePolicy{};needsInference:=false;for _,node:=range nodes{if dispatch[node.reference()].infer!=nil{needsInference=true}}
 if needsInference{inferencePolicies,err=readInferencePolicies();if err!=nil{return nil,err}}
 processName:="";processDigest:=""
 if boundary!=nil {processName,_=boundary.ref["name"].(string);processDigest,_=boundary.ref["digest"].(string)}
 publicReadUsed:=false
 for _,node:=range nodes {
  entry:=dispatch[node.reference()];if entry.scoped==nil && entry.scopedConfig==nil && entry.infer==nil {continue}
  raw,err:=readNodeSpec(entry.spec);if err!=nil{return nil,err}
  var spec struct { Dimensions []struct {Name string;Contents json.RawMessage}; Identity struct {Name string} }
  if err:=json.Unmarshal(raw,&spec);err!=nil{return nil,err}
  if entry.infer!=nil {
   if f.publicReadPipe {return nil,fmt.Errorf("selected public-read pipe does not transport inference")}
   p,ok:=inferencePolicies[node.key];if !ok{return nil,fmt.Errorf("inference policy absent")};digest,err:=p.Digest();if err!=nil{return nil,err}
   var selection []string
   if b,ok:=bindings[node.key];ok {if b.Operation!="infer"||len(b.Origins)>0||b.CredentialRef!="" {return nil,fmt.Errorf("invalid inference selection")};selection=b.Routes;usedBindings[node.key]=true}
   selected:=map[string]bool{};for _,name:=range selection{selected[name]=true}
   for _,b:=range p.Bindings {if selection!=nil&&!selected[b.ID]{continue};if b.CredentialRef==""{continue};matches:=0;for i,g:=range grants {if g.NodeKey==node.key&&g.Operation==b.ID {if g.PolicyDigest!=digest||g.Ref!=b.CredentialRef{return nil,fmt.Errorf("inference grant binding mismatch")};used[i]=true;matches++}};if matches!=1{return nil,fmt.Errorf("node %s: exact inference binding grant required",node.key)}}
   sum:=sha256.Sum256(raw);id:=egress.Identity{RunID:runID,ProcessDigest:comp,NodeKey:node.key,NodeName:node.node,NodeDigest:node.digest,NodeSpecDigest:hex.EncodeToString(sum[:]),PolicyDigest:digest,Operation:"infer"}
   var handle egress.InvocationHandle
   if f.providerHTTPSPipe {
    policyRaw,marshalErr:=json.Marshal(p);if marshalErr!=nil{return nil,marshalErr};policySum:=sha256.Sum256(policyRaw)
    claim:=egress.ProviderHTTPSClaim{PackageDigest:f.providerHTTPSPackageDigest,ProcessName:processName,ProcessDigest:processDigest,EventID:f.eventID,NodeKey:node.key,NodeName:node.node,NodeDigest:node.digest,NodeSpecDigest:hex.EncodeToString(sum[:]),PolicyDigest:hex.EncodeToString(policySum[:])}
    handle,err=egress.StartInferenceWithTransport(context.Background(),id,p,selection,provider,func(b egress.ModelBinding)egress.TargetAdapter{selectedClaim:=claim;selectedClaim.Operation=b.ID;return egress.ProviderHTTPSPipe{Client:providerHTTPSPipe,Claim:selectedClaim}})
   } else {handle,err=egress.StartInference(context.Background(),id,p,selection,provider)}
   if err!=nil{return nil,err};handles[node.key]=handle;continue
  }
  var policy egress.Policy
  var policyRaw json.RawMessage
  for _,d:=range spec.Dimensions {if d.Name=="egress" {policyRaw=append(json.RawMessage(nil),d.Contents...);policy,err=egress.Parse(d.Contents);if err!=nil{return nil,err}}}
  if len(policy.Operations)==0 {return nil,fmt.Errorf("scoped Node needs an operation")}
  if f.publicReadPipe {
   if policy.Mode==egress.ModeBrowser || policy.Mode==egress.ModeLoopbackHTTP || policy.Mode==egress.ModeInfer {return nil,fmt.Errorf("selected public-read pipe does not transport %s operations",policy.Mode)}
   if policy.Mode==egress.ModeHTTPS {
    if spec.Identity.Name!=node.node {return nil,fmt.Errorf("public-read Node spec identity mismatch")}
    for _,operation:=range policy.Operations {if operation.Destination.Kind!=egress.DestinationPublicWeb || operation.Method!="GET" || len(operation.Credentials)!=0 || operation.MaxRedirects!=0 || operation.RedirectPolicy!=egress.RedirectNone || len(operation.QueryParameters)!=0 {return nil,fmt.Errorf("selected public-read pipe permits only credential-free redirect-free public GET operations")}}
    publicReadUsed=true
   }
  }
  digest,err:=policy.Digest();if err!=nil{return nil,err}
  sum:=sha256.Sum256(raw);specDigest:=hex.EncodeToString(sum[:])
  operationHandles:=map[string]egress.InvocationHandle{}
  for _,operation:=range policy.Operations {
   binding:=egress.Binding{Operation:operation.ID}
   bindingKey:=node.key;if len(policy.Operations)>1{bindingKey=node.key+"/"+operation.ID}
   if selected,ok:=bindings[bindingKey];ok {if selected.Operation!=operation.ID{cancelOperationHandles(policy,operationHandles);return nil,fmt.Errorf("binding operation mismatch")};binding=selected;usedBindings[bindingKey]=true}
   for i,g:=range grants {if g.NodeKey==node.key&&g.Operation==operation.ID {
    if g.PolicyDigest!=digest {cancelOperationHandles(policy,operationHandles);return nil,fmt.Errorf("credential grant policy mismatch")}
    if binding.CredentialRef!="" {cancelOperationHandles(policy,operationHandles);return nil,fmt.Errorf("ambiguous operation credential grant")}
    binding.CredentialRef=g.Ref;used[i]=true
   }}
   id:=egress.Identity{RunID:runID,ProcessDigest:comp,NodeKey:node.key,NodeName:node.node,NodeDigest:node.digest,NodeSpecDigest:specDigest,PolicyDigest:digest,Operation:operation.ID}
   var adapter egress.TargetAdapter=egress.NativeHTTPS{};if policy.Mode==egress.ModeGoInputs{adapter=egress.NativeGoInputs{}};if policy.Mode==egress.ModeGoCommand{adapter=egress.NativeGoCommand{}};if policy.Mode==egress.ModeLoopbackHTTP{adapter=egress.NativeLoopbackHTTP{}};if policy.Mode==egress.ModeSQLite{adapter=egress.NativeSQLite{}};if policy.Mode==egress.ModeFilesystem{adapter=egress.NativeFilesystem{}};if policy.Mode==egress.ModeWorkspaceDelivery{adapter=egress.NativeWorkspaceDelivery{}};if policy.Mode==egress.ModeRecordStore{adapter=egress.NativeRecordStore{}};if policy.Mode==egress.ModeAtomicFile{adapter=egress.NativeAtomicFile{}};if policy.Mode==egress.ModeConditionalRecord{adapter=egress.NativeConditionalRecord{}};if policy.Mode==egress.ModeNativeExec{adapter=egress.NativeExecutable{}};if policy.Mode==egress.ModeBrowser{adapter=browserAdapter}
   if f.publicReadPipe && policy.Mode==egress.ModeHTTPS {
    policySum:=sha256.Sum256(policyRaw)
    claim:=egress.PublicReadClaim{PackageDigest:f.publicReadPackageDigest,ProcessName:processName,ProcessDigest:processDigest,EventID:f.eventID,NodeKey:node.key,NodeName:node.node,NodeDigest:node.digest,NodeSpecDigest:specDigest,PolicyDigest:hex.EncodeToString(policySum[:]),Operation:operation.ID}
    adapter=egress.PublicReadPipeHTTPS{Client:publicReadPipe,Claim:claim}
   }
   if f.providerHTTPSPipe && policy.Mode==egress.ModeHTTPS {
    policySum:=sha256.Sum256(policyRaw)
    claim:=egress.ProviderHTTPSClaim{PackageDigest:f.providerHTTPSPackageDigest,ProcessName:processName,ProcessDigest:processDigest,EventID:f.eventID,NodeKey:node.key,NodeName:node.node,NodeDigest:node.digest,NodeSpecDigest:specDigest,PolicyDigest:hex.EncodeToString(policySum[:]),Operation:operation.ID}
    adapter=egress.ProviderHTTPSPipe{Client:providerHTTPSPipe,Claim:claim}
   }
   if f.providerHTTPSPipe && (policy.Mode==egress.ModeBrowser || policy.Mode==egress.ModeLoopbackHTTP || policy.Mode==egress.ModeInfer) {return nil,fmt.Errorf("selected provider HTTPS pipe cannot transport %s",policy.Mode)}
   configurationDigest,configurationErr:=egress.ConfigurationSelectionDigest(node.configuration);if configurationErr!=nil{return nil,configurationErr};id.ConfigurationDigest=configurationDigest
   handle,err:=egress.StartBound(context.Background(),adapter,egress.Invocation{Identity:id,Policy:policy,Binding:binding,Credentials:provider});if err!=nil{
    cancelOperationHandles(policy,operationHandles)
    return nil,fmt.Errorf("node %s operation %s: %v",node.key,operation.ID,err)
   }
   operationHandles[operation.ID]=handle
  }
  if len(operationHandles)==1 {handles[node.key]=operationHandles[policy.Operations[0].ID]} else {handles[node.key]=&multiOperationHandle{handles:operationHandles}}
 }
 if f.publicReadPipe && !publicReadUsed {return nil,fmt.Errorf("selected public-read pipe found no declared public GET operation")}
 if len(used)!=len(grants) {return nil,fmt.Errorf("credential grant names an unscoped or unknown node")}
 if len(usedBindings)!=len(bindings) {return nil,fmt.Errorf("network binding names an unscoped or unknown node")}
 good=true;return handles,nil
}

func cancelEgressHandles(handles map[string]egress.InvocationHandle) {
 keys:=make([]string,0,len(handles));for key:=range handles{keys=append(keys,key)};sort.Strings(keys)
 for _,key:=range keys {handles[key].Cancel()}
}

func cancelOperationHandles(policy egress.Policy, handles map[string]egress.InvocationHandle) {
 for _,operation:=range policy.Operations {if handle:=handles[operation.ID];handle!=nil{handle.Cancel()}}
}

// --- harness dispatch (F1) -----------------------------------------------
type harnessContractView struct {
	Harness           string
	Binary            string
	PromptDelivery    string
	PromptFileFlag    string
	Parser            string
	ModelFromRoute    bool
	CallWorkdir       bool
	Interactivity     string
	CredentialChannel string
	CredentialAuth    string
	OneshotArgv       []string
}

type harnessApproval struct {
	receipt map[string]any
	parked  map[string]any
}

func (e *harnessApproval) Error() string { return "harness round awaits approval" }

type harnessResumeRecord struct {
	RoundID        string
	LoopKey        string
	NodeKey        string
	PromptDigest   string
	ContractDigest string
	TokenEnv       string
}

var harnessResume *harnessResumeRecord

// harnessResumeMu serializes resume claims. A claim consumes the pending
// record only for the parked round it names: parallel siblings and later
// rounds never observe it, so PRAXIS_APPROVAL_TOKEN reaches exactly the
// sanctioned resumed job and nothing after it.
var harnessResumeMu sync.Mutex

func claimHarnessResume(roundID string) *harnessResumeRecord {
	harnessResumeMu.Lock()
	defer harnessResumeMu.Unlock()
	if harnessResume == nil || harnessResume.RoundID != roundID {
		return nil
	}
	record := harnessResume
	harnessResume = nil
	return record
}

const harnessTimeoutDefault = 900

// harnessOutputCapBytes bounds the combined stdout+stderr the worker retains
// from one harness child (mirrors the egress response cap): overflow cancels
// the job and refuses the receipt.
const harnessOutputCapBytes = 16 << 20

func harnessTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("PRAXIS_HARNESS_TIMEOUT_SECONDS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return harnessTimeoutDefault * time.Second
}

func specDocument(path string) (map[string]any, error) {
	raw, err := readNodeSpec(path)
	if err != nil {
		return nil, err
	}
	decoded, err := contractschema.DecodeJSON(raw)
	if err != nil {
		return nil, err
	}
	doc, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec must be an object")
	}
	return doc, nil
}

// specIdentityKind reads the declared node kind. A spec without an
// identity block yields the empty kind: older sealed prints never shipped
// one, and only harness binding and loop declarations need the type.
func specIdentityKind(path string) (string, error) {
	doc, err := specDocument(path)
	if err != nil {
		return "", err
	}
	identity, _ := doc["identity"].(map[string]any)
	kind, _ := identity["kind"].(string)
	return kind, nil
}

func specDimension(doc map[string]any, name string) map[string]any {
	list, _ := doc["dimensions"].([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if entry["name"] != name {
			continue
		}
		contents, _ := entry["contents"].(map[string]any)
		return contents
	}
	return nil
}

func specSurfaceTargets(doc map[string]any, side string) map[string]string {
	out := map[string]string{}
	surface := specDimension(doc, "surface")
	list, _ := surface[side].([]any)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		field, _ := entry["field"].(string)
		target, _ := entry["target"].(string)
		if field != "" && target != "" {
			out[field] = target
		}
	}
	return out
}

// loadHarnessContracts reads the printed contract asset. Each contract
// ships as the exact generated document text so the worker recomputes the
// byte digest admission pinned; nothing is re-encoded.
type harnessContractAsset struct {
	Contracts map[string]string
}

func loadHarnessContracts() (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join("worker", "harness.json"))
	if err != nil {
		return nil, fmt.Errorf("harness contract asset missing")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc harnessContractAsset
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("harness contract asset invalid")
	}
	if len(doc.Contracts) == 0 {
		return nil, fmt.Errorf("harness contract asset empty")
	}
	return doc.Contracts, nil
}

func harnessContractDocument(text string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("harness contract document invalid")
	}
	return doc, nil
}

func harnessContractViewOf(doc map[string]any) harnessContractView {
	view := harnessContractView{}
	view.Harness, _ = doc["harness"].(string)
	view.Binary, _ = doc["binary"].(string)
	view.PromptDelivery, _ = doc["prompt_delivery"].(string)
	view.PromptFileFlag, _ = doc["prompt_file_flag"].(string)
	view.Parser, _ = doc["parser"].(string)
	view.ModelFromRoute, _ = doc["model_from_route"].(bool)
	view.CallWorkdir, _ = doc["call_workdir"].(bool)
	view.Interactivity, _ = doc["interactivity"].(string)
	view.CredentialChannel, _ = doc["credential_channel"].(string)
	view.CredentialAuth, _ = doc["credential_auth"].(string)
	if list, ok := doc["oneshot_argv"].([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok {
				view.OneshotArgv = append(view.OneshotArgv, s)
			}
		}
	}
	return view
}

func harnessContractDigest(text string) string {
	return bytesDigest([]byte(text), "praxis.harness_contract.v1")
}

func harnessTargetFields(targets map[string]string) []string {
	fields := make([]string, 0, len(targets))
	for field := range targets {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

func harnessPrompt(inputs map[string]any, targets map[string]string) string {
	for _, field := range harnessTargetFields(targets) {
		if targets[field] != "slot:prompt" {
			continue
		}
		if value, ok := inputs[field].(string); ok && value != "" {
			return value
		}
	}
	if value, ok := inputs["prompt"].(string); ok {
		return value
	}
	return ""
}

func harnessSlot(inputs map[string]any, targets map[string]string, slot string) string {
	for _, field := range harnessTargetFields(targets) {
		if targets[field] != "slot:"+slot {
			continue
		}
		if value, ok := inputs[field].(string); ok && value != "" {
			return value
		}
	}
	if value, ok := inputs[slot].(string); ok {
		return value
	}
	return ""
}

var harnessPlaceholders = []string{"<prompt>", "<model>", "<workspace>", "<prompt-file>", "<workdir>"}

// harnessCheckPlaceholders refuses unknown placeholder-shaped tokens in one
// template element before substitution. Substituted values may then carry
// any text (including '<') without tripping the check; only the pinned
// template's own vocabulary is policed.
func harnessCheckPlaceholders(value string) error {
	for i := 0; i < len(value); i++ {
		if value[i] != '<' {
			continue
		}
		end := -1
		for j := i + 1; j < len(value); j++ {
			if value[j] == '>' {
				end = j
				break
			}
			if value[j] == '<' {
				break
			}
		}
		if end < 0 {
			continue
		}
		token := value[i : end+1]
		known := false
		for _, ph := range harnessPlaceholders {
			if token == ph {
				known = true
				break
			}
		}
		if known {
			i = end
			continue
		}
		if harnessPlaceholderShaped(token) {
			return fmt.Errorf("harness argv placeholder unknown")
		}
	}
	return nil
}

func harnessPlaceholderShaped(token string) bool {
	if len(token) < 3 || token[0] != '<' || token[len(token)-1] != '>' {
		return false
	}
	for _, c := range token[1 : len(token)-1] {
		if c != '-' && c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// harnessChildEnv is the minimal environment for the external harness
// child: path, home, and temp only. Everything else the job needs arrives
// as argv, files, or the explicit resume token — never as inherited secrets.
func harnessChildEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir()}
}

// harnessArgv builds the exact argv from the pinned template: no shell, no
// invented flags, placeholders substituted from the contract and inputs.
func harnessArgv(template []any, contract harnessContractView, prompt, model, workspace, promptFile, workdir string) ([]string, error) {
	if len(template) == 0 {
		return nil, fmt.Errorf("harness argv template entry")
	}
	argv := make([]string, 0, len(template))
	values := map[string]string{
		"<prompt>":      prompt,
		"<model>":       model,
		"<workspace>":   workspace,
		"<prompt-file>": promptFile,
		"<workdir>":     workdir,
	}
	for i, item := range template {
		value, ok := item.(string)
		if !ok || value == "" {
			return nil, fmt.Errorf("harness argv template entry")
		}
		if i == 0 {
			if value == "<binary>" {
				value = contract.Binary
			}
			if value != contract.Binary {
				return nil, fmt.Errorf("harness argv binary drift")
			}
			argv = append(argv, value)
			continue
		}
		if err := harnessCheckPlaceholders(value); err != nil {
			return nil, err
		}
		argv = append(argv, harnessSubstituteText(value, values))
	}
	return argv, nil
}

// harnessSubstituteText replaces the pinned placeholders in one left-to-right
// pass over the template element. A placeholder is consumed as a unit and the
// substituted value is copied verbatim, never rescanned: a prompt carrying
// "<model>" or "<workdir>" arrives byte-identical to its digest binding.
func harnessSubstituteText(template string, values map[string]string) string {
	var out strings.Builder
	out.Grow(len(template))
	for i := 0; i < len(template); {
		if template[i] != '<' {
			out.WriteByte(template[i])
			i++
			continue
		}
		end := i + 1
		for end < len(template) && template[end] != '>' && template[end] != '<' {
			end++
		}
		if end >= len(template) || template[end] == '<' {
			out.WriteByte(template[i])
			i++
			continue
		}
		token := template[i : end+1]
		if replacement, ok := values[token]; ok {
			out.WriteString(replacement)
		} else {
			out.WriteString(token)
		}
		i = end + 1
	}
	return out.String()
}

// parseHarnessOutput maps stdout to the parsed result and its text payload
// by the contract parser. Unknown or unparseable shapes are errors: the
// caller records a failed receipt instead of guessing a result.
func parseHarnessOutput(parser string, raw []byte) (map[string]any, string, error) {
	decode := func(data []byte) (map[string]any, bool) {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var value any
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		obj, ok := value.(map[string]any)
		return obj, ok
	}
	switch parser {
	case "json_object":
		obj, ok := decode(raw)
		if !ok {
			return nil, "", fmt.Errorf("output_shape")
		}
		return obj, harnessText(obj, string(raw)), nil
	case "codex_jsonl", "muse_jsonl", "opencode_jsonl":
		var last map[string]any
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line[0] != '{' {
				continue
			}
			if obj, ok := decode([]byte(line)); ok {
				last = obj
			}
		}
		if last == nil {
			return nil, "", fmt.Errorf("output_shape")
		}
		return last, harnessText(last, string(raw)), nil
	case "plain", "text":
		text := strings.TrimRight(string(raw), "\r\n")
		return map[string]any{"text": text}, text, nil
	}
	return nil, "", fmt.Errorf("output_shape")
}

func harnessText(obj map[string]any, fallback string) string {
	for _, key := range []string{"text", "content", "result", "output", "final"} {
		if value, ok := obj[key].(string); ok && value != "" {
			return value
		}
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return fallback
	}
	return string(raw)
}

func harnessResultDigest(raw []byte) string {
	return bytesDigest(raw, "praxis.harness.output.v1")
}

func harnessRoundID(loopKey string, round int, nodeKey, promptDigest, contractDigest string) (string, bool) {
	return canonDigest(map[string]any{"loop": loopKey, "round": round, "node_key": nodeKey,
		"prompt_digest": promptDigest, "contract_digest": contractDigest}, "praxis.harness_round.v1")
}

func writeParkedRound(record map[string]any) error {
	if err := os.MkdirAll(".praxis-approvals", 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	roundID, _ := record["round_id"].(string)
	if roundID == "" {
		return fmt.Errorf("parked round identity missing")
	}
	return os.WriteFile(filepath.Join(".praxis-approvals", roundID+".json"), append(raw, '\n'), 0o600)
}

func loadParkedRound(roundID string) (*harnessResumeRecord, map[string]any, error) {
	if strings.ContainsAny(roundID, "/\\") || roundID == "" || roundID == "." || roundID == ".." {
		return nil, nil, fmt.Errorf("resume round identity shape")
	}
	raw, err := os.ReadFile(filepath.Join(".praxis-approvals", roundID+".json"))
	if err != nil {
		return nil, nil, fmt.Errorf("parked round not found")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("parked round corrupt")
	}
	rec := &harnessResumeRecord{}
	rec.RoundID, _ = doc["round_id"].(string)
	rec.LoopKey, _ = doc["loop"].(string)
	rec.NodeKey, _ = doc["node_key"].(string)
	rec.PromptDigest, _ = doc["prompt_digest"].(string)
	rec.ContractDigest, _ = doc["contract_digest"].(string)
	if rec.RoundID != roundID || rec.NodeKey == "" {
		return nil, nil, fmt.Errorf("parked round identity mismatch")
	}
	return rec, doc, nil
}

// harnessCapture bounds the combined child stdout+stderr the worker retains.
// Both streams share one budget; the stream that overflows marks the capture
// and cancels the job, so a runaway child can neither exhaust worker memory
// nor smuggle an oversized payload into a receipt.
type harnessCapture struct {
	mu     sync.Mutex
	limit  int
	total  int
	over   bool
	cancel context.CancelFunc
	stdout bytes.Buffer
	stderr bytes.Buffer
}

type harnessCaptureStream struct {
	capture *harnessCapture
	dst     *bytes.Buffer
}

func (w *harnessCaptureStream) Write(p []byte) (int, error) {
	c := w.capture
	c.mu.Lock()
	room := c.limit - c.total
	if room > len(p) {
		room = len(p)
	}
	if room > 0 {
		w.dst.Write(p[:room])
		c.total += room
	}
	if room < len(p) {
		c.over = true
		c.mu.Unlock()
		c.cancel()
		return len(p), nil
	}
	c.mu.Unlock()
	return len(p), nil
}

func (c *harnessCapture) stdoutStream() io.Writer {
	return &harnessCaptureStream{capture: c, dst: &c.stdout}
}

func (c *harnessCapture) stderrStream() io.Writer {
	return &harnessCaptureStream{capture: c, dst: &c.stderr}
}

// harnessInvoke is the entry.harness dispatch arm: one CLI job, one closed
// receipt. The returned map is the declared output mapping; the receipt is
// the type-specific execution receipt. The loop executor calls this arm
// outside the worker's per-node recover, so a panic here is converted to a
// classified error instead of escaping into a worker crash.
func harnessInvoke(specPath string, inputs map[string]any) (outputs map[string]any, receipt map[string]any, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "harness dispatch panic stack:\n%s\n", debug.Stack())
			outputs = nil
			receipt = nil
			err = fmt.Errorf("harness dispatch panic: %v", r)
		}
	}()
	doc, err := specDocument(specPath)
	if err != nil {
		return nil, nil, err
	}
	handoff := specDimension(doc, "handoff")
	if handoff == nil {
		return nil, nil, fmt.Errorf("harness handoff missing")
	}
	if handoff["executor"] == "sdk" { return harnessSDKInvoke(doc, handoff, inputs, specSurfaceTargets(doc, "inputs")) }
	name, _ := handoff["harness"].(string)
	contractRef, _ := handoff["contract"].(map[string]any)
	wantDigest, _ := contractRef["digest"].(string)
	contracts, err := loadHarnessContracts()
	if err != nil {
		return nil, nil, err
	}
	contractText, ok := contracts[name]
	if !ok {
		return nil, nil, fmt.Errorf("harness contract %s not shipped", name)
	}
	if "sha256:"+harnessContractDigest(contractText) != wantDigest {
		return nil, nil, fmt.Errorf("harness contract digest drift")
	}
	contractDoc, err := harnessContractDocument(contractText)
	if err != nil {
		return nil, nil, err
	}
	contract := harnessContractViewOf(contractDoc)
	if contract.Binary == "" || contract.Parser == "" || !harnessKnownInteractivity(contract.Interactivity) {
		return nil, nil, fmt.Errorf("harness contract vocabulary invalid")
	}
	targets := map[string]string{}
	for field, target := range specSurfaceTargets(doc, "inputs") {
		targets[field] = target
	}
	prompt := harnessPrompt(inputs, targets)
	if prompt == "" && contract.PromptDelivery != "stdin" {
		return nil, nil, fmt.Errorf("harness prompt missing")
	}
	model := harnessSlot(inputs, targets, "model")
	if contract.ModelFromRoute && model == "" {
		return nil, nil, fmt.Errorf("harness model route missing")
	}
	workspace := harnessSlot(inputs, targets, "workspace")
	if workspace == "" {
		workspace = strings.TrimSpace(os.Getenv("PRAXIS_HARNESS_WORKSPACE"))
	}
	workdir, err := os.MkdirTemp("", "praxis-harness-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(workdir)
	if workspace == "" {
		workspace, err = os.MkdirTemp("", "praxis-harness-workspace-*")
		if err != nil {
			return nil, nil, err
		}
		defer os.RemoveAll(workspace)
	} else if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, nil, err
	}
	promptFile := filepath.Join(workdir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(prompt), 0o600); err != nil {
		return nil, nil, err
	}
	template, _ := handoff["argv_template"].([]any)
	argv, err := harnessArgv(template, contract, prompt, model, workspace, promptFile, workdir)
	if err != nil {
		return nil, nil, err
	}
	promptDigest := bytesDigest([]byte(prompt), "praxis.harness.prompt.v1")
	keyString, _ := inputs["__node_key"].(string)
	loopKey, _ := inputs["__loop_key"].(string)
	roundIndex := harnessInt(inputs["__round_index"], 0)
	delete(inputs, "__node_key")
	delete(inputs, "__loop_key")
	delete(inputs, "__round_index")
	roundID, ok := harnessRoundID(loopKey, roundIndex, keyString, promptDigest, wantDigest)
	if !ok {
		return nil, nil, fmt.Errorf("harness round undigestible")
	}
	resumeRecord := claimHarnessResume(roundID)
	resumed := resumeRecord != nil
	parkedPath := filepath.Join(".praxis-approvals", roundID+".json")
	if _, statErr := os.Stat(parkedPath); statErr == nil && !resumed {
		return nil, nil, fmt.Errorf("parked round refuses automatic retry; resume explicitly with -resume %s -approval-token-env NAME", roundID)
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, nil, fmt.Errorf("harness argv template entry")
	}
	ctx, cancel := context.WithTimeout(context.Background(), harnessTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// WaitDelay bounds the post-kill wait for a child that inherited the
	// output pipe: a bound that blocks must park at the bound, not hang.
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = workspace
	// The external harness child never inherits the worker environment:
	// deploy keys, tokens, and credentials stay with the worker. Only the
	// minimal locale/path/temp base rides along, plus the explicit resume
	// approval token on the sanctioned resumed round, claimed exactly once.
	cmd.Env = harnessChildEnv()
	if resumeRecord != nil && resumeRecord.TokenEnv != "" {
		if token := os.Getenv(resumeRecord.TokenEnv); token != "" {
			cmd.Env = append(cmd.Env, "PRAXIS_APPROVAL_TOKEN="+token)
		}
	}
	var stdin string
	if contract.PromptDelivery == "stdin" {
		stdin = prompt
	}
	cmd.Stdin = strings.NewReader(stdin)
	capture := &harnessCapture{limit: harnessOutputCapBytes, cancel: cancel}
	cmd.Stdout = capture.stdoutStream()
	cmd.Stderr = capture.stderrStream()
	runErr := cmd.Run()
	out := capture.stdout.Bytes()
	receipt = map[string]any{
		"harness":         name,
		"contract_digest": wantDigest,
		"prompt_digest":   promptDigest,
		"result_digest":   harnessResultDigest(out),
		"status":          "succeeded",
	}
	park := func(reason string) error {
		receipt["status"] = "awaiting_approval"
		receipt["error_class"] = reason
		parked := map[string]any{
			"round_id": roundID, "loop": loopKey, "node_key": keyString,
			"prompt_digest": promptDigest, "contract_digest": wantDigest,
			"status": "awaiting_approval", "reason": reason,
		}
		if err := writeParkedRound(parked); err != nil {
			return err
		}
		return &harnessApproval{receipt: receipt, parked: parked}
	}
	if capture.over {
		receipt["status"] = "failed"
		receipt["error_class"] = "output_overflow"
		return nil, receipt, fmt.Errorf("harness output exceeds %d bytes", harnessOutputCapBytes)
	}
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			if contract.Interactivity == "prompts" {
				return nil, receipt, park("approval_bound")
			}
			receipt["status"] = "failed"
			receipt["error_class"] = "timeout"
			return nil, receipt, fmt.Errorf("harness job timed out")
		}
		receipt["status"] = "failed"
		receipt["error_class"] = "process"
		return nil, receipt, fmt.Errorf("harness job failed")
	}
	result, _, err := parseHarnessOutput(contract.Parser, out)
	if err != nil {
		receipt["status"] = "failed"
		receipt["error_class"] = "output_shape"
		return nil, receipt, err
	}
	if usage, ok := result["usage"]; ok {
		receipt["usage"] = usage
	}
	if resumed {
		// The resume is its own labeled receipt; the parked record served
		// its purpose and is cleared so a later fresh run is not blocked.
		receipt["resumed"] = true
		receipt["round_id"] = roundID
		if err := os.Remove(parkedPath); err != nil && !os.IsNotExist(err) {
			return nil, receipt, fmt.Errorf("parked round not cleared")
		}
	}
	mapping := specSurfaceTargets(doc, "outputs")
	outputs = map[string]any{}
	for field, target := range mapping {
		key, ok := strings.CutPrefix(target, "result:")
		if !ok {
			return outputs, receipt, fmt.Errorf("harness output target %s", target)
		}
		switch key {
		case "text":
			outputs[field] = harnessText(result, "")
		case "json":
			outputs[field] = result
		default:
			if value, ok := result[key]; ok {
				outputs[field] = value
			}
		}
	}
	return outputs, receipt, nil
}

func harnessKnownInteractivity(value string) bool {
	return value == "prompts" || value == "non_interactive"
}

func harnessInt(value any, fallback int) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func bindHarnessArms(nodes []processNode) error {
	for _, node := range nodes {
		entry, ok := dispatch[node.reference()]
		if !ok {
			continue
		}
		if entry.harness != nil {
			continue
		}
		kind, err := specIdentityKind(entry.spec)
		if err != nil {
			return fmt.Errorf("node %s: %v", node.key, err)
		}
		if kind == "harness" {
			entry.harness = harnessInvoke
			dispatch[node.reference()] = entry
		}
	}
	return nil
}

type harnessSDKFailure struct{ cause error; receipt map[string]any }
func(e *harnessSDKFailure) Error()string{return e.cause.Error()}
func(e *harnessSDKFailure) Unwrap()error{return e.cause}
func(e *harnessSDKFailure) FailureRecord()map[string]any{return map[string]any{"code":"harness.sdk.execution_failed","identity":map[string]any{"attempt":e.receipt}}}

// Only the launcher's pre-start refusal establishes an absent SDK effect.
// A failed or interrupted child that actually started remains uncertain.
func settledSDKStartupFailure(doc,evidence map[string]any)bool{
 identity,_:=doc["identity"].(map[string]any);if identity["kind"]!="harness"{return false}
 composition,_:=doc["composition"].(map[string]any);effects,_:=composition["effects"].([]any);if len(effects)!=1||effects[0]!="harness.invoke"{return false}
 handoff:=specDimension(doc,"handoff");if handoff["executor"]!="sdk"{return false}
 driver,_:=evidence["driver"].(map[string]any);if driver["type"]!="harness"{return false};attempt,_:=driver["receipt"].(map[string]any)
 started,known:=attempt["started"].(bool);if !known||started||attempt["executor"]!="sdk"||attempt["status"]!="failed"||attempt["effect_outcome"]!="not_started"||attempt["harness"]!=handoff["harness"]{return false}
 class,ok:=attempt["error_class"].(string);if !ok||class==""{return false}
 for _,field:=range []string{"stdout_digest","stderr_digest","partial_stdout","diagnostics"}{if _,present:=attempt[field];present{return false}}
 return true
}

func harnessSDKInvoke(doc, handoff map[string]any, inputs map[string]any, targets map[string]string) (outputs map[string]any, receipt map[string]any, err error) {
 name,_:=handoff["harness"].(string)
 sdk,ok:=handoff["sdk"].(map[string]any);if !ok{return nil,nil,fmt.Errorf("SDK declaration missing")}
 get:=func(key string)string{v,_:=sdk[key].(string);return v}
 receipt=map[string]any{"harness":name,"executor":"sdk","status":"failed","started":false,"effect_outcome":"not_started"}
 fail:=func(class string,cause error)(map[string]any,map[string]any,error){receipt["error_class"]=class;return nil,receipt,&harnessSDKFailure{cause:cause,receipt:receipt}}
 contractBytes,err:=os.ReadFile(get("contract_file"));if err!=nil{return fail("contract",err)}
 var contractEnvelope map[string]map[string]string;if err=json.Unmarshal(contractBytes,&contractEnvelope);err!=nil{return fail("contract",err)}
 raw,ok:=contractEnvelope["contracts"][name];if !ok{return fail("contract",fmt.Errorf("SDK contract not shipped"))}
 ref,_:=handoff["contract"].(map[string]any);if ref["digest"]!="sha256:"+harnessContractDigest(raw){return fail("contract",fmt.Errorf("SDK contract changed"))}
 contract,err:=harnessContractDocument(raw);if err!=nil{return fail("contract",err)}
 declared,_:=contract["sdk"].(map[string]any);packages,_:=declared["packages"].(map[string]any);entries,_:=declared["entrypoints"].(map[string]any)
 if packages[get("language")]!=get("package")||entries[get("language")]!=get("entrypoint"){return fail("contract",fmt.Errorf("SDK contract selection changed"))}
 sha:=func(raw []byte)string{sum:=sha256.Sum256(raw);return hex.EncodeToString(sum[:])}
 files,ok:=sdk["files"].(map[string]any);if !ok{return fail("material",fmt.Errorf("SDK material absent"))}
 for file,pin:=range files{if filepath.IsAbs(file)||filepath.Clean(file)!=file||strings.HasPrefix(file,"../")||strings.Contains(file,"\\"){return fail("material",fmt.Errorf("SDK material path invalid"))};raw,e:=os.ReadFile(file);if e!=nil||"sha256:"+sha(raw)!=pin{return fail("material",fmt.Errorf("SDK material changed: %s",file))}}
 runtimePin,ok:=sdk["runtime"].(map[string]any);if !ok{return fail("runtime",fmt.Errorf("SDK runtime absent"))}
 binary,_:=runtimePin["binary"].(string)
 selected:="";runtimeRoot:=""
 if bundle,_:=runtimePin["bundle_file"].(string);bundle!=""{
  pin,_:=runtimePin["bundle_digest"].(string);target,_:=runtimePin["target"].(string)
  var cleanup func();runtimeRoot,selected,cleanup,err=egress.PrepareSDKRuntime(bundle,pin,binary,target);if err!=nil{return fail("runtime",err)};defer cleanup()
 }else{selected,err=exec.LookPath(binary);if err!=nil{return fail("runtime",err)}}
 binaryRaw,err:=os.ReadFile(selected);if err!=nil||sha(binaryRaw)!=runtimePin["sha256"]{return fail("runtime",fmt.Errorf("SDK runtime differs from Node pin"))}
 runner,err:=filepath.Abs(get("runner_file"));if err!=nil{return fail("material",err)}
 if files[get("runner_file")]!=get("runner_digest"){return fail("material",fmt.Errorf("SDK runner pin changed"))}
 args:=[]string{};for _,raw:=range runtimePin["arguments"].([]any){value,ok:=raw.(string);if !ok{return fail("runtime",fmt.Errorf("SDK runtime argument shape"))};args=append(args,value)};args=append(args,runner)
 request:=map[string]any{"harness":name,"distribution":get("package")}
 for _,key:=range []string{"operation","version","module","entrypoint","adapter","adapter_digest","runner_digest","contract_digest","bindings_digest"}{request[key]=get(key)}
 for field,target:=range targets{slot,ok:=strings.CutPrefix(target,"slot:");if !ok{return fail("input",fmt.Errorf("SDK input must bind an explicit slot"))};if _,exists:=request[slot];exists{return fail("input",fmt.Errorf("SDK input cannot replace implementation identity"))};request[slot]=inputs[field]}
 body,err:=json.Marshal(request);if err!=nil{return fail("input",err)}
 receipt["request_digest"]=sha(body);receipt["runtime_digest"]=runtimePin["sha256"]
 ctx,stopSignals:=egress.SDKInvocationContext(context.Background());defer stopSignals();cancel:=func(){};defer func(){cancel()}()
 if millis,ok:=runtimePin["timeout_ms"].(json.Number);ok{n,e:=millis.Int64();if e!=nil||n<0{return fail("runtime",fmt.Errorf("SDK timeout shape"))};if n>0{ctx,cancel=context.WithTimeout(ctx,time.Duration(n)*time.Millisecond)}}
 cmd:=exec.CommandContext(ctx,selected,args...)
 if err:=egress.ConfigureSDKCommand(cmd);err!=nil{return fail("runtime",err)}
 if slot,_:=sdk["working_directory_slot"].(string);slot!=""{dir,_:=request[slot].(string);info,e:=os.Stat(dir);if !filepath.IsAbs(dir)||e!=nil||!info.IsDir(){return fail("input",fmt.Errorf("SDK working directory must be an existing absolute directory"))};cmd.Dir=dir}
 cmd.Env=harnessChildEnv()
 if runtimeRoot!=""{for i,value:=range cmd.Env{if strings.HasPrefix(value,"PATH="){cmd.Env[i]="PATH="+filepath.Join(runtimeRoot,"bin")+string(os.PathListSeparator)+strings.TrimPrefix(value,"PATH=")}};cmd.Env=append(cmd.Env,"PRAXIS_SDK_RUNTIME_ROOT="+runtimeRoot)}
 secrets:=[]string{}
 if names,ok:=sdk["environment"].([]any);ok{for _,raw:=range names{key,ok:=raw.(string);if !ok{return fail("environment",fmt.Errorf("SDK environment shape"))};if value,present:=os.LookupEnv(key);present{cmd.Env=append(cmd.Env,key+"="+value);if value!=""{secrets=append(secrets,value)}}}}
 redact:=func(value string)string{sort.Slice(secrets,func(i,j int)bool{return len(secrets[i])>len(secrets[j])});for _,secret:=range secrets{value=strings.ReplaceAll(value,secret,"[redacted-credential]")};return value}
 var stdout,stderr bytes.Buffer
 outLog:=egress.NewSDKDiagnostics(os.Stderr,secrets);errLog:=egress.NewSDKDiagnostics(os.Stderr,secrets)
 defer outLog.Close();defer errLog.Close()
 cmd.Stdin=bytes.NewReader(body);cmd.Stdout=io.MultiWriter(&stdout,outLog);cmd.Stderr=io.MultiWriter(&stderr,errLog)
 retainAttempt:=func(){raw,_:=json.Marshal(receipt);fmt.Fprintln(os.Stderr,"PRAXIS_SDK_ATTEMPT:"+string(raw))}
 retainAttempt()
 if err=cmd.Start();err!=nil{return fail("startup",err)}
 receipt["started"]=true;receipt["effect_outcome"]="unknown"
 retainAttempt()
 err=cmd.Wait()
 receipt["stdout_digest"]=sha(stdout.Bytes());receipt["stderr_digest"]=sha(stderr.Bytes());receipt["diagnostics"]=redact(stderr.String())
 if err!=nil{receipt["partial_stdout"]=redact(stdout.String());return fail("process",fmt.Errorf("SDK operation failed: %w",err))}
 result,err:=contractschema.DecodeJSON(stdout.Bytes());if err!=nil{receipt["partial_stdout"]=redact(stdout.String());return fail("output_shape",err)}
 resultObject,ok:=result.(map[string]any);if !ok{return fail("output_shape",fmt.Errorf("SDK output must be an object"))}
 outputs=map[string]any{}
 for field,target:=range specSurfaceTargets(doc,"outputs"){key,ok:=strings.CutPrefix(target,"result:");if !ok{return fail("output_mapping",fmt.Errorf("SDK output mapping invalid"))};if key=="json"{outputs[field]=resultObject}else{value,present:=resultObject[key];if !present{return fail("output_mapping",fmt.Errorf("SDK output field missing: %s",key))};outputs[field]=value}}
 receipt["status"]="succeeded";receipt["effect_outcome"]="returned"
 return outputs,receipt,nil
}

// --- loop executor (F1) --------------------------------------------------
type workerLoopDecl struct {
	Key         string
	DriverKind  string
	DriverKey   string
	AttemptsMax int
	ToolsMax    int
	Tools       []string
	Escalate    bool
	EscalateOn  []string
	Exception egress.LoopExceptionContract
}

var workerLoopSignals = map[string]bool{"output_invalid": true, "context_full": true, "agent_judgment": true}

func parseWorkerLoops(processDoc map[string]any, nodes []processNode, kinds map[string]string) ([]workerLoopDecl, error) {
 value,present:=processDoc["loops"];if !present{return nil,nil}
 raw,err:=json.Marshal(value);if err!=nil{return nil,err}
 definitions:=map[string]egress.LoopNodeDefinition{}
 for _,node:=range nodes{definitions[node.key]=egress.LoopNodeDefinition{Kind:kinds[node.key],Digest:node.digest}}
 declarations,err:=egress.ParseLoopDeclarations(raw,definitions);if err!=nil{return nil,err}
 loops:=make([]workerLoopDecl,0,len(declarations))
 for _,wire:=range declarations{loops=append(loops,workerLoopDecl{Key:wire.Key,DriverKind:wire.Driver.Kind,DriverKey:wire.Driver.NodeKey,AttemptsMax:wire.AttemptsMax,ToolsMax:wire.ToolsMax,Tools:wire.Tools,Escalate:wire.Escalate,EscalateOn:wire.EscalateOn,Exception:wire.Exception})}
 return loops,nil
}

func workerLoopTruthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "false"
	case json.Number:
		n, err := v.Float64()
		return err == nil && n != 0
	case float64:
		return v != 0
	}
	return false
}

func workerLoopNumber(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return 0
		}
		return n
	case int:
		return float64(v)
	case string:
		n, err := json.Number(v).Float64()
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

func workerLoopDecision(out map[string]any, tools, signals []string) (string, string) {
	for _, signal := range signals {
		if workerLoopTruthy(out[signal]) {
			return "escalate", ""
		}
	}
	action, _ := out["next_action"].(string)
	switch action {
	case "", "escalate":
		return "escalate", ""
	case "finish":
		split, _ := out["split"].(string)
		if split != "sufficient" || harnessInt(out["completeness"], -1) != 3 {
			return "escalate", ""
		}
		if workerLoopNumber(out["confidence"]) < 0.85 {
			return "escalate", ""
		}
		return "finish", ""
	}
	for _, key := range tools {
		if action == key {
			if workerLoopNumber(out["confidence"]) < 0.625 {
				return "escalate", ""
			}
			return "continue", key
		}
	}
	return "escalate", ""
}

func workerLoopDecisionRecord(out map[string]any) map[string]any {
	record := map[string]any{}
	for _, key := range []string{"next_action", "completeness", "split", "confidence", "act_probability"} {
		if value, ok := out[key]; ok {
			record[key] = value
		}
	}
	return record
}

func workerLoopEscalateReason(out map[string]any, signals []string) string {
	for _, signal := range signals {
		if workerLoopTruthy(out[signal]) {
			return "signal " + signal
		}
	}
	action, _ := out["next_action"].(string)
	if action == "" {
		return "decision absent"
	}
	return "decision " + action
}

func runWorkerLoops(loops []workerLoopDecl, nodes []processNode, specs map[string][]map[string]any,
	results map[string]map[string]any, bv boundView, capabilities map[string]egress.InvocationHandle,
	failures map[string]string) ([]map[string]any, error) {
	var receipts []map[string]any
	for _, loop := range loops {
		var driver *processNode
		for i := range nodes {
			if nodes[i].key == loop.DriverKey {
				driver = &nodes[i]
			}
		}
		if driver == nil {
			return receipts, fmt.Errorf("loop %s driver missing", loop.Key)
		}
		state := map[string]any{}
		for key, value := range driver.fills {
			state[key] = value
		}
		rounds := []any{}
		outcome := "escalate"
		reason := "attempts exhausted"
		var lastOut map[string]any
		for round := 0; round < loop.AttemptsMax; round++ {
			overlay := map[string]any{}
			for key, value := range state {
				overlay[key] = value
			}
			overlay["__node_key"] = loop.DriverKey
			overlay["__loop_key"] = loop.Key
			overlay["__round_index"] = round
			out, err := runNodeWith(nodes, nil, specs, nil, loop.DriverKey, bv, capabilities[loop.DriverKey], overlay)
			record := map[string]any{
				"round_index": round,
				"driver":      map[string]any{"kind": loop.DriverKind, "node_key": loop.DriverKey},
			}
			if receipt, ok := driverReceipts.Load(loop.DriverKey); ok {
				record["driver_receipt"] = receipt
			}
			if err != nil {
				if park, ok := err.(*harnessApproval); ok {
					return receipts, park
				}
				record["outcome"] = "escalate"
				record["error"] = err.Error()
				if d, ok := canonDigest(map[string]any{"error": err.Error()}, "praxis.loop_round_result.v1"); ok {
					record["result_digest"] = d
				}
				rounds = append(rounds, record)
				outcome = "escalate"
				reason = "driver failed: " + err.Error()
				break
			}
			lastOut = out
			if norm, ok := canonDigest(normJSON(out), "praxis.loop_round_result.v1"); ok {
				record["result_digest"] = norm
			}
			if loop.DriverKind == "harness" {
				// The harness's own continuation is the decision: a job that
				// returned cleanly finishes the round, receipted as-is.
				if err:=loop.Exception.Accept(out);err!=nil {record["outcome"]="escalate";record["error"]=err.Error();rounds=append(rounds,record);reason=err.Error();outcome="escalate";break}
				record["outcome"] = "finish"
				rounds = append(rounds, record)
				outcome = "finish"
				break
			}
			record["decision"] = workerLoopDecisionRecord(out)
			next, tool := workerLoopDecision(out, loop.Tools, loop.EscalateOn)
			if next=="finish" {if err:=loop.Exception.Accept(out);err!=nil {next="escalate";record["error"]=err.Error();reason=err.Error()}}
			if next == "continue" {
				record["tool"] = tool
				toolOverlay := map[string]any{}
				for key, value := range state {
					toolOverlay[key] = value
				}
				for key, value := range out {
					toolOverlay[key] = value
				}
				toolOut, err := runNodeWith(nodes, nil, specs, nil, tool, bv, capabilities[tool], toolOverlay)
				if err != nil {
					record["outcome"] = "escalate"
					record["error"] = err.Error()
					rounds = append(rounds, record)
					outcome = "escalate"
					reason = "tool " + tool + " failed: " + err.Error()
					break
				}
				if digest, ok := canonDigest(normJSON(toolOut), "praxis.loop_round_result.v1"); ok {
					record["result_digest"] = digest
				}
				// Fresh-context merge: the stale driver output lands first and
				// the fresh tool artifacts land last, so a tool result always
				// wins a collision. The round record carries the receipt
				// artifacts (decision subset, digests, outcome); state carries
				// the surviving values into the next round's overlay.
				for key, value := range out {
					state[key] = value
				}
				for key, value := range toolOut {
					state[key] = value
				}
				record["outcome"] = "continue"
				rounds = append(rounds, record)
				continue
			}
			record["outcome"] = next
			rounds = append(rounds, record)
			outcome = next
			if next == "escalate" {
				if acceptanceErr,ok:=record["error"].(string);ok {reason=acceptanceErr} else {reason = workerLoopEscalateReason(out, loop.EscalateOn)}
			}
			break
		}
		if outcome == "continue" {
			outcome = "escalate"
			reason = "attempts exhausted"
		}
		if lastOut != nil {
			results[loop.DriverKey] = lastOut
		}
		body := map[string]any{
			"outcome_id":loop.Exception.Outcome.ID,
			"loop":    loop.Key,
			"driver":  map[string]any{"kind": loop.DriverKind, "node_key": loop.DriverKey},
			"rounds":  rounds,
			"outcome": outcome,
		}
		if lastOut != nil {
			if digest, ok := canonDigest(normJSON(lastOut), "praxis.loop_result.v1"); ok {
				body["result_digest"] = digest
			}
		}
		if outcome == "escalate" {
			body["reason"] = reason
			failures[loop.DriverKey] = "loop " + loop.Key + " escalated: " + reason
		}
		receipts = append(receipts, body)
	}
	return receipts, nil
}

type runtimeRecoveryLocation struct {Block string `json:"block"`;Node string `json:"node"`}
type runtimeRecoveryBinding struct {
 Semantics string `json:"semantics,omitempty"`
 Target runtimeRecoveryLocation `json:"target"`
 Reconciler struct {Key string `json:"key"`;Ref any `json:"ref"`;Block struct {Schema string `json:"schema"`;Name string `json:"name"`;Nodes []struct {Key string `json:"key"`;Node any `json:"node"`;Fills map[string]any `json:"fills,omitempty"`;Configuration *string `json:"configuration,omitempty"`} `json:"nodes"`;Edges []any `json:"edges,omitempty"`;Inputs []struct{Name string `json:"name"`;To struct{Node string `json:"node"`;Port string `json:"port"`} `json:"to"`} `json:"inputs,omitempty"`;Outputs []struct{Name string `json:"name"`;From struct{Node string `json:"node"`;Port string `json:"port"`} `json:"from"`} `json:"outputs,omitempty"`} `json:"block"`;Fills map[string]any `json:"fills,omitempty"`;Parameters map[string]any `json:"parameters,omitempty"`} `json:"reconciler"`
 Inputs map[string]string `json:"inputs"`
 Outputs map[string]string `json:"outputs"`
 Runtime map[string]string `json:"runtime,omitempty"`
}
type runtimeRecoveryPolicy struct {Schema string `json:"schema,omitempty"`;Bindings []runtimeRecoveryBinding `json:"bindings"`}
type recoveryUnknown struct {message string;failure map[string]any};func(e *recoveryUnknown)Error()string{return e.message};func(e *recoveryUnknown)FailureRecord()map[string]any{return e.failure}
// A restored sealed error retains its exact normalized structural identity.
type sealedCheckpointFailure struct{display string;fields map[string]any}
func(e *sealedCheckpointFailure)Error()string{return e.display}
func(e *sealedCheckpointFailure)FailureRecord()map[string]any{return e.fields}
func restoreCheckpointFailure(out *executionjournal.NodeOutcome)error{
 if len(out.Failure)==0{return fmt.Errorf("%s",out.Error)}
 var fields map[string]any;if err:=decodeProcessJSON(out.Failure,&fields);err!=nil{return &checkpointFailure{err}}
 return &sealedCheckpointFailure{display:out.Error,fields:fields}
}
type checkpointFailure struct {cause error};func(e *checkpointFailure)Error()string{return "recovery checkpoint refused: "+e.cause.Error()};func(e *checkpointFailure)Unwrap()error{return e.cause}
type recoveryRun struct {
 settledCheckpoints []runtimeSettledCheckpointBinding
 checkpointObservations sync.Map
 claim *executionjournal.Journal
 replacement *receiptReplacementNoop
 activationKeys map[string]string
 policy *runtimeRecoveryPolicy
 process map[string]any
 sources map[string]any
 evidence sync.Map
 origins sync.Map
 probeResults sync.Map
 probeFailures sync.Map
 runtimeValues map[string]string
 activationOrdinals map[string]int
 activationHandles map[string]egress.InvocationHandle
 activationNodes []processNode
}

// reconcilerRuntimeFills derives the ORIGINAL target activation's journal
// identity for one reconciler probe, using the exact ordinal map the runtime
// sequence bindings were assigned from. Nothing is caller-supplied and no
// value is guessed.
func (r *recoveryRun)reconcilerRuntimeFills(targetKey string,runtimeMap map[string]string,checkpoint map[string]any)(map[string]any,error){
	if len(runtimeMap)==0{return nil,nil}
	if r.runtimeValues==nil||r.runtimeValues["event_id"]==""||r.runtimeValues["invocation_id"]==""{return nil,fmt.Errorf("reconciler runtime inputs require event identity")}
	ordinal,ok:=r.activationOrdinals[targetKey]
	if !ok||ordinal==0{return nil,fmt.Errorf("target activation ordinal is absent")}
	out:=map[string]any{}
	for field,source:=range runtimeMap{
		switch source{
		case "target_event_id":out[field]=r.runtimeValues["event_id"]
		case "target_invocation_id":out[field]=r.runtimeValues["invocation_id"]
		case "target_sequence":out[field]=ordinal
		case "target_event":out[field]=map[string]any{"event_id":r.runtimeValues["event_id"],"invocation_id":r.runtimeValues["invocation_id"],"sequence":ordinal}
        case "target_checkpoint":if checkpoint==nil{return nil,fmt.Errorf("target checkpoint is unavailable")};value,err:=detachValues(checkpoint);if err!=nil{return nil,err};out[field]=value
		default:return nil,fmt.Errorf("reconciler runtime source %q is unsupported",source)
		}
	}
	return out,nil
}
// targetCheckpointSnapshot is derived once while the original journal claim
// is held. Artifact strings retain unknown effects, accounting and exact bytes.
// No receipt, event or permission supplied by a caller enters this projection.
func(r *recoveryRun)targetCheckpointSnapshot(targetKey,checkpointKey,digest string)(map[string]any,error){
 if r.claim==nil{return nil,fmt.Errorf("target checkpoint requires the journal claim")}
 checkpoint,err:=r.claim.DurableNode(checkpointKey);if err!=nil{return nil,err}
 if checkpoint==nil||checkpoint.Key!=checkpointKey||checkpoint.InputDigest!=digest{return nil,fmt.Errorf("target checkpoint identity differs")}
 if checkpoint.State!="started"&&checkpoint.State!="settled"{return nil,fmt.Errorf("target checkpoint state is invalid")}
 if (checkpoint.State=="settled")!=(checkpoint.Outcome!=nil){return nil,fmt.Errorf("target checkpoint outcome is inconsistent")}
 ordinal,ok:=r.activationOrdinals[targetKey];if !ok||ordinal<1||r.runtimeValues["event_id"]==""||r.runtimeValues["invocation_id"]==""{return nil,fmt.Errorf("target checkpoint intrinsic event is unavailable")}
 attempts:=[]any{};for _,attempt:=range checkpoint.Attempts{attempts=append(attempts,map[string]any{"error":attempt.Error,"failure_json":string(attempt.Failure),"receipts_json":string(attempt.Receipts)})}
 outcome:=map[string]any{"present":checkpoint.Outcome!=nil,"outputs_json":"","error":"","failure_json":"","receipts_json":"","resolution_json":""}
 if checkpoint.Outcome!=nil{outcome["outputs_json"]=string(checkpoint.Outcome.Outputs);outcome["error"]=checkpoint.Outcome.Error;outcome["failure_json"]=string(checkpoint.Outcome.Failure);outcome["receipts_json"]=string(checkpoint.Outcome.Receipts);outcome["resolution_json"]=string(checkpoint.Outcome.Resolution)}
 return map[string]any{"schema":"praxis.recovery_target_checkpoint.v1","target":targetKey,"activation":checkpointKey,"event":map[string]any{"event_id":r.runtimeValues["event_id"],"invocation_id":r.runtimeValues["invocation_id"],"sequence":ordinal},"input_digest":digest,"state":checkpoint.State,"attempts":attempts,"outcome":outcome},nil
}
func newRecoveryRun(boundary *processRun,claim *executionjournal.Journal)*recoveryRun{
 r:=&recoveryRun{};if boundary!=nil && (boundary.recovery!=nil || boundary.progression) && claim!=nil {r.claim=claim;r.policy=boundary.recovery;r.process=boundary.ref;r.sources=boundary.sources};return r
}
func recoveryKey(block,node string)string{return strconv.Itoa(len(block))+":"+block+node}
func nodeInputs(nodes []processNode,edges []processEdge,results map[string]map[string]any,key string)(map[string]any,error){
 inputs:=map[string]any{};found:=false;for _,n:=range nodes{if n.key==key{found=true;for name,value:=range n.fills{inputs[name]=value}}};if !found{return nil,fmt.Errorf("unknown Node")}
 for _,edge:=range edges{if edge.toNode!=key{continue};up,ok:=results[edge.fromNode];if !ok{return nil,fmt.Errorf("missing predecessor")};if value,ok:=up[edge.fromField];ok{inputs[edge.toField]=value}}
 return detachValues(inputs)
}
// SHA-256 of the exact canonical Go JSON input object is supplied to the
// pinned reconciler; the service adapter must echo its durable request identity.
func recoveryInputDigest(inputs map[string]any)(string,error){raw,err:=json.Marshal(inputs);if err!=nil{return "",err};sum:=sha256.Sum256(raw);return hex.EncodeToString(sum[:]),nil}
func executionEvidence(key string,handle egress.InvocationHandle)(map[string]any,error){
 result:=map[string]any{};if observation,ok:=nodeInvocationObservations.Load(key);ok{result["invocation"]=observation};if handle!=nil{var receipts any=handle.Receipt();if multi,ok:=handle.(interface{Receipts() []egress.Receipt});ok{receipts=multi.Receipts()};raw,err:=json.Marshal(receipts);if err!=nil{return nil,err};value,err:=valuecontract.Decode(raw);if err!=nil{return nil,err};result["egress"]=value};if raw,ok:=driverReceipts.Load(key);ok{result["driver"]=map[string]any{"type":"harness","receipt":raw}};return result,nil
}
func validateRecoveredOutputs(fields []map[string]any,key string,values map[string]any,bv boundView)error{
 expected:=map[string]bool{};for _,field:=range fields{if field["side"]!="output"{continue};name,_:=field["name"].(string);expected[name]=true;value,present:=values[name];required,_:=field["required"].(bool);if !present{if required{return fmt.Errorf("recovered output %s missing",name)};continue};if err:=checkValue(field,value,bv.forKey(key,"output",name));err!=nil{return fmt.Errorf("recovered output %s: %v",name,err)}};for name:=range values{if !expected[name]{return fmt.Errorf("undeclared recovered output")}};return nil
}
func invokeRecoverySafe(nodes []processNode,edges []processEdge,specs map[string][]map[string]any,results map[string]map[string]any,key string,bv boundView,handle egress.InvocationHandle,overlay map[string]any)(out map[string]any,err error){
 defer func(){if value:=recover();value!=nil{out=nil;err=fmt.Errorf("adapter panic: %v",value)}}();return runNodeWith(nodes,edges,specs,results,key,bv,handle,overlay)
}
func(r *recoveryRun)execute(nodes []processNode,edges []processEdge,specs map[string][]map[string]any,results map[string]map[string]any,key string,bv boundView,handles map[string]egress.InvocationHandle)(map[string]any,error){
 if r.claim==nil{return runNode(nodes,edges,specs,results,key,bv,handles[key])}
 inputs,err:=nodeInputs(nodes,edges,results,key);if err!=nil{return nil,&checkpointFailure{err}};injectRuntimeInputs(nodes,key,inputs);digest,err:=recoveryInputDigest(inputs);if err!=nil{return nil,&checkpointFailure{err}}
 checkpoint,err:=r.claim.Node(key);if err!=nil{return nil,&checkpointFailure{err}}
 if checkpoint!=nil{
  if checkpoint.InputDigest!=digest{return nil,&checkpointFailure{fmt.Errorf("Node input identity changed")}}
  if checkpoint.State=="settled"{
   if checkpoint.Outcome==nil{return nil,&checkpointFailure{fmt.Errorf("settled Node lacks outcome")}}
   var evidence map[string]any;if len(checkpoint.Outcome.Receipts)>0{if err:=decodeProcessJSON(checkpoint.Outcome.Receipts,&evidence);err!=nil{return nil,&checkpointFailure{err}}};effectOutcome,_:=evidence["effect_outcome"].(map[string]any);delete(evidence,"effect_outcome");r.evidence.Store(key,evidence)
   origin:=map[string]any{"origin":"restored","request_digest":digest};if effectOutcome!=nil{origin["origin"]="not_sent";origin["effect_outcome"]=effectOutcome};if len(checkpoint.Outcome.Resolution)>0{var resolution map[string]any;if err:=decodeProcessJSON(checkpoint.Outcome.Resolution,&resolution);err!=nil{return nil,&checkpointFailure{err}};origin["resolution"]=resolution};r.origins.Store(key,origin)
   if resolution,ok:=origin["resolution"].(map[string]any);ok{probeKey,_:=resolution["reconciler"].(string);if output,ok:=resolution["outputs"].(map[string]any);ok{r.probeResults.Store(probeKey,output);restoredOrigin:=map[string]any{"origin":"restored_reconciliation","request_digest":digest};if snapshot,ok:=resolution["target_checkpoint"];ok{restoredOrigin["target_checkpoint"]=snapshot;restoredOrigin["reconciler_input_digest"]=resolution["reconciler_input_digest"]};r.origins.Store(probeKey,restoredOrigin);if evidence,ok:=resolution["evidence"].(map[string]any);ok{r.evidence.Store(probeKey,evidence)}}}
   if checkpoint.Outcome.Error!=""{return nil,restoreCheckpointFailure(checkpoint.Outcome)}
   var output map[string]any;if err:=decodeProcessJSON(checkpoint.Outcome.Outputs,&output);err!=nil{return nil,&checkpointFailure{err}};if err:=validateRecoveredOutputs(specs[key],key,output,bv);err!=nil{return nil,&checkpointFailure{err}};return output,nil
  }
  if checkpoint.State!="started"{return nil,&checkpointFailure{fmt.Errorf("unknown Node checkpoint state")}}
  pure:=false;for _,node:=range nodes{if node.key==key{entry:=dispatch[node.reference()];doc,err:=specDocument(entry.spec);if err!=nil{return nil,&checkpointFailure{err}};identity,_:=doc["identity"].(map[string]any);composition,_:=doc["composition"].(map[string]any);effects,_:=composition["effects"].([]any);pure=identity["kind"]=="function" && len(effects)==0}}
  if !pure{return r.reconcile(nodes,edges,specs,results,key,inputs,digest,bv,handles)}
 }
 if err:=r.claim.StartNode(key,digest);err!=nil{return nil,&checkpointFailure{err}}
 out,runErr:=invokeRecoverySafe(nodes,edges,specs,results,key,bv,handles[key],nil)
 evidence,evidenceErr:=executionEvidence(key,handles[key]);if evidenceErr!=nil{return nil,&checkpointFailure{evidenceErr}}
 var noEffect map[string]any
 if runErr!=nil {noEffect=recoveryNotSentOutcome(nodes,key,runErr,evidence,inputs,digest,r.process,r.sources[key])}
 if noEffect!=nil {evidence["effect_outcome"]=noEffect}
 rawEvidence,err:=json.Marshal(evidence);if err!=nil{return nil,&checkpointFailure{err}}
 var rawFailure json.RawMessage
 if runErr!=nil{rawFailure,err=json.Marshal(failureFields(runErr));if err!=nil{return nil,&checkpointFailure{err}}}
 if runErr!=nil && noEffect==nil && uncertainRecoveryFailure(nodes,key,evidence){
  if err:=r.claim.RecordNodeAttempt(key,digest,executionjournal.NodeAttempt{Error:runErr.Error(),Failure:rawFailure,Receipts:rawEvidence});err!=nil{return nil,&checkpointFailure{err}}
  fields:=failureFields(runErr);r.evidence.Store(key,evidence);r.origins.Store(key,map[string]any{"origin":"unknown","request_digest":digest,"failure":fields});return nil,&recoveryUnknown{message:"effect may have occurred; reconciliation is required",failure:fields}
 }
 outcome:=executionjournal.NodeOutcome{Receipts:rawEvidence};if runErr!=nil{outcome.Error=runErr.Error();outcome.Failure=rawFailure}else{outcome.Outputs,err=json.Marshal(out);if err!=nil{return nil,&checkpointFailure{err}}}
 if err:=r.claim.SettleNode(key,digest,outcome);err!=nil{return nil,&checkpointFailure{err}}
 if noEffect!=nil {delete(evidence,"effect_outcome")};r.evidence.Store(key,evidence);origin:=map[string]any{"origin":"executed","request_digest":digest};if noEffect!=nil{origin["origin"]="not_sent";origin["effect_outcome"]=noEffect};r.origins.Store(key,origin);return out,runErr
}
func(r *recoveryRun)reconcile(nodes []processNode,edges []processEdge,specs map[string][]map[string]any,results map[string]map[string]any,key string,inputs map[string]any,digest string,bv boundView,handles map[string]egress.InvocationHandle)(map[string]any,error){
 return r.reconcileAt(nodes,edges,specs,results,key,key,inputs,digest,bv,handles)
}
func(r *recoveryRun)reconcileAt(nodes []processNode,edges []processEdge,specs map[string][]map[string]any,results map[string]map[string]any,key,checkpointKey string,inputs map[string]any,digest string,bv boundView,handles map[string]egress.InvocationHandle)(map[string]any,error){
 if r.policy==nil {return nil,&recoveryUnknown{message:"effect has no pinned reconciliation",failure:failureFields(nil)}}
 for _,binding:=range r.policy.Bindings{
  if recoveryKey(binding.Target.Block,binding.Target.Node)!=key{continue}
  if len(binding.Reconciler.Block.Nodes)!=1{return nil,&checkpointFailure{fmt.Errorf("invalid sealed reconciliation shape")}}
  probeKey:=recoveryKey(binding.Reconciler.Key,binding.Reconciler.Block.Nodes[0].Key)
  overlay:=map[string]any{};for _,port:=range binding.Reconciler.Block.Inputs{if port.Name=="request_digest"{overlay[port.To.Port]=digest}else if source,ok:=binding.Inputs[port.Name];ok{if value,ok:=inputs[source];ok{overlay[port.To.Port]=value}}}
  probeRuntime:=map[string]string{};for _,node:=range nodes{if node.key==probeKey&&node.recoveryOnly{probeRuntime=node.runtime}}
  var targetCheckpoint map[string]any
  for _,source:=range probeRuntime{if source=="target_checkpoint"{var snapshotErr error;targetCheckpoint,snapshotErr=r.targetCheckpointSnapshot(key,checkpointKey,digest);if snapshotErr!=nil{return nil,&checkpointFailure{snapshotErr}};break}}
  fills,fillErr:=r.reconcilerRuntimeFills(key,probeRuntime,targetCheckpoint);if fillErr!=nil{return nil,&checkpointFailure{fillErr}}
  for name,value:=range fills{if _,exists:=overlay[name];exists{return nil,&checkpointFailure{fmt.Errorf("reconciler runtime input %s conflicts with a bound input",name)}};overlay[name]=value}
  probeInputDigest:=""
  if targetCheckpoint!=nil{var inputErr error;probeInputs,inputErr:=nodeInputs(nodes,nil,nil,probeKey);if inputErr!=nil{return nil,&checkpointFailure{inputErr}};for name,value:=range overlay{probeInputs[name]=value};probeInputDigest,inputErr=recoveryInputDigest(probeInputs);if inputErr!=nil{return nil,&checkpointFailure{inputErr}}}
  probe,err:=invokeRecoverySafe(nodes,nil,specs,results,probeKey,bv,handles[probeKey],overlay)
  evidence,evidenceErr:=executionEvidence(probeKey,handles[probeKey]);if evidenceErr!=nil{return nil,&checkpointFailure{evidenceErr}};r.evidence.Store(probeKey,evidence);probeOrigin:=map[string]any{"origin":"reconciliation","request_digest":digest};if targetCheckpoint!=nil{probeOrigin["target_checkpoint"]=targetCheckpoint;probeOrigin["reconciler_input_digest"]=probeInputDigest};r.origins.Store(probeKey,probeOrigin)
  if err!=nil{r.probeFailures.Store(probeKey,failureFields(err));return nil,r.noteUnknown(checkpointKey,digest,"reconciliation failed; original effect remains unknown",err,probeKey,evidence)}
  r.probeResults.Store(probeKey,probe)
  exposed:=map[string]any{};for _,port:=range binding.Reconciler.Block.Outputs{if value,ok:=probe[port.From.Port];ok{exposed[port.Name]=value}}
  if exposed["status"]!="completed"{return nil,r.noteUnknown(checkpointKey,digest,"reconciliation could not prove completion",nil,probeKey,evidence)}
  if exposed["request_digest"]!=digest{return nil,r.noteUnknown(checkpointKey,digest,"reconciliation request identity mismatch",nil,probeKey,evidence)}
  out:=map[string]any{};for target,source:=range binding.Outputs{if value,ok:=exposed[source];ok{out[target]=value}}
  if err:=validateRecoveredOutputs(specs[key],key,out,bv);err!=nil{return nil,r.noteUnknown(checkpointKey,digest,err.Error(),err,probeKey,evidence)}
  probeReceipt:=map[string]any{"node":probeKey,"state":"succeeded","outputs":probe,"source":r.sources[probeKey],"process":r.process,"execution":probeOrigin}
  for name,value:=range evidence{probeReceipt[name]=value}
  resolution:=map[string]any{"prior_state":"started","request_digest":digest,"reconciler":probeKey,"outputs":probe,"evidence":evidence,"receipt":probeReceipt};if targetCheckpoint!=nil{resolution["target_checkpoint"]=targetCheckpoint;resolution["reconciler_input_digest"]=probeInputDigest}
  var checkpoint *executionjournal.NodeCheckpoint;var checkpointErr error;if targetCheckpoint!=nil{checkpoint,checkpointErr=r.claim.DurableNode(checkpointKey)}else{checkpoint,checkpointErr=r.claim.Node(checkpointKey)};if checkpointErr!=nil{return nil,&checkpointFailure{checkpointErr}};if checkpoint!=nil{attempts:=append([]executionjournal.NodeAttempt(nil),checkpoint.Attempts...);if attempts==nil&&targetCheckpoint!=nil{attempts=[]executionjournal.NodeAttempt{}};resolution["original_attempts"]=attempts}
  rawResolution,err:=json.Marshal(resolution);if err!=nil{return nil,&checkpointFailure{err}}
  if err:=decodeProcessJSON(rawResolution,&resolution);err!=nil{return nil,&checkpointFailure{err}}
  if err:=r.claim.SettleNode(checkpointKey,digest,executionjournal.NodeOutcome{Outputs:mustRecoveryJSON(out),Resolution:rawResolution});err!=nil{return nil,&checkpointFailure{err}}
  r.evidence.Store(checkpointKey,map[string]any{});r.origins.Store(checkpointKey,map[string]any{"origin":"reconciled","request_digest":digest,"resolution":resolution});return out,nil
 }
 fields:=failureFields(nil);fields["display"]="interrupted effect has no configured reconciliation capability"
 r.origins.Store(checkpointKey,map[string]any{"origin":"unknown","request_digest":digest,"failure":fields});return nil,&recoveryUnknown{message:"interrupted effect has no configured reconciliation capability",failure:fields}
}
func(r *recoveryRun)noteUnknown(key,digest,message string,cause error,probeKey string,evidence map[string]any)error{
 fields:=failureFields(cause);if message!=""{fields["display"]=message}
 raw,err:=json.Marshal(map[string]any{"reconciler":probeKey,"evidence":evidence,"failure":fields});if err!=nil{return &checkpointFailure{err}}
 rawFailure,err:=json.Marshal(fields);if err!=nil{return &checkpointFailure{err}}
 if err:=r.claim.RecordNodeAttempt(key,digest,executionjournal.NodeAttempt{Error:message,Failure:rawFailure,Receipts:raw});err!=nil{return &checkpointFailure{err}}
 r.origins.Store(key,map[string]any{"origin":"unknown","request_digest":digest,"failure":fields});return &recoveryUnknown{message:message,failure:fields}
}
// Actual scoped executor receipts distinguish completed failed calls from
// uncertain effects. These values come from the kernel handle, not Node input
// or a claimed failure disposition. A sealed failure never becomes success.
func settledRecoveryFailure(nodes []processNode,key string,evidence map[string]any)bool{
 var effects []any;found:=false
 for _,node:=range nodes{if node.key!=key{continue};found=true;entry,ok:=dispatch[node.reference()];if !ok{return false};doc,err:=specDocument(entry.spec);if err!=nil{return false};composition,ok:=doc["composition"].(map[string]any);if !ok{return false};effects,ok=composition["effects"].([]any);if !ok{return false};identity,_:=doc["identity"].(map[string]any);if settledSDKStartupFailure(doc,evidence){return true};if len(effects)==0&&identity["kind"]!="function"{return false};break}
 if !found{return false};if len(effects)==0{return true}
 readOnly:=true;atomicOnly:=true
 for _,effect:=range effects{switch effect{case "infer.api","decision.api","network.read","filesystem.read":default:readOnly=false};if effect!="filesystem.read"&&effect!="filesystem.write"{atomicOnly=false}}
 if !readOnly&&!atomicOnly{return false}
 actual,present:=evidence["egress"];if !present||actual==nil{return false}
 raw,err:=json.Marshal(actual);if err!=nil{return false};receipts:=[]egress.Receipt{}
 if len(raw)>0&&raw[0]=='['{if err:=replacementRuntimeDecode(raw,&receipts);err!=nil{return false}}else{var receipt egress.Receipt;if err:=replacementRuntimeDecode(raw,&receipt);err!=nil{return false};receipts=append(receipts,receipt)}
 if len(receipts)==0{return false}
 for _,receipt:=range receipts{
  id:=receipt.Identity
  if id.NodeKey!=key||id.NodeName==""||id.NodeSpecDigest==""||id.NodeDigest==""||id.ProcessDigest==""||id.RunID==""||id.PolicyDigest==""||receipt.PolicyDigest!=id.PolicyDigest||receipt.Operation!=id.Operation||receipt.Requests<0||receipt.Responses<0||receipt.DeniedRequests<0{return false}
  if receipt.Requests==0&&receipt.Responses==0&&receipt.Transmission=="not_sent"&&(receipt.Outcome=="unused"||receipt.Outcome=="refused"||receipt.Outcome=="failed"){continue}
  if readOnly&&receipt.Requests>0&&receipt.Responses==receipt.Requests&&receipt.Transmission=="response_received"&&(receipt.Outcome=="completed"||receipt.Outcome=="succeeded"||receipt.Outcome=="failed"||receipt.Outcome=="refused"){continue}
  a:=receipt.AtomicFile
  if !atomicOnly||receipt.Adapter!="native.atomic_file.v1"||a==nil||a.Schema!="praxis.atomic_file_receipt.v1"||receipt.Requests!=1||receipt.Transmission!="sent"{return false}
  switch a.Method{case "REPLACE":if a.Write==nil{return false};case "ACKNOWLEDGE":if a.Acknowledgement==nil{return false};case "READ","RECONCILE":default:return false}
  if receipt.Outcome=="succeeded"&&receipt.Responses==1{continue}
  if receipt.Outcome!="failed"||receipt.Responses!=0||a.FailureStage==""{return false}
 };return true
}
func uncertainRecoveryFailure(nodes []processNode,key string,evidence map[string]any)bool{
 if settledRecoveryFailure(nodes,key,evidence){return false}
 var effects []any;for _,node:=range nodes{if node.key==key{doc,err:=specDocument(dispatch[node.reference()].spec);if err!=nil{return true};composition,_:=doc["composition"].(map[string]any);effects,_=composition["effects"].([]any);if readOnlySQLiteEffects(effects,doc){return false}}}
 if len(effects)==0{return false};for _,effect:=range effects{if effect=="filesystem.write" || effect=="harness.invoke"{return true}}
 raw,_:=json.Marshal(evidence["egress"]);var receipt struct{Requests int};if len(raw)>0 && raw[0]=='['{var receipts []struct{Requests int};if json.Unmarshal(raw,&receipts)!=nil{return true};for _,receipt:=range receipts{if receipt.Requests>0{return true}};return false};if json.Unmarshal(raw,&receipt)!=nil{return true};return receipt.Requests>0
}
func readOnlySQLiteEffects(effects []any,doc map[string]any)bool{
 if len(effects)==0{return false};for _,effect:=range effects{if effect!="filesystem.read"{return false}}
 policy:=specDimension(doc,"egress");if policy["mode"]!="sqlite"{return false};operations,ok:=policy["operations"].([]any);if !ok||len(operations)==0{return false}
 for _,raw:=range operations{operation,ok:=raw.(map[string]any);if !ok||operation["method"]!="READ"{return false}}
 return true
}
func recoveryNotSentOutcome(nodes []processNode,key string,runErr error,evidence,inputs map[string]any,digest string,process map[string]any,source any)map[string]any{
 effectFailure,ok:=runErr.(interface{ProcessEffectOutcome()string});if !ok||effectFailure.ProcessEffectOutcome()!="not_sent"||process==nil||source==nil{return nil}
 pinned:=false;for _,node:=range nodes{if node.key!=key{continue};doc,err:=specDocument(dispatch[node.reference()].spec);if err!=nil{return nil};identity,_:=doc["identity"].(map[string]any);pinned=identity["name"]=="businessledger.commit"&&identity["kind"]=="integration";break};if !pinned{return nil}
 egress,ok:=evidence["egress"];if !ok{return nil};egressDigest,ok:=canonDigest(egress,"praxis.node.egress.receipts.v1");if !ok{return nil}
 eventID:="";if request,ok:=inputs["commit_request"].(map[string]any);ok{eventID,_=request["event_id"].(string)}
 errorDigest:=sha256.Sum256([]byte(runErr.Error()))
 return map[string]any{"schema":"praxis.node_effect_outcome.v1","outcome":"not_sent","process":process,"node":key,"source":source,"request_digest":digest,"event_id":eventID,"egress_receipts_digest":egressDigest,"error_digest":hex.EncodeToString(errorDigest[:])}
}
func mustRecoveryJSON(value any)json.RawMessage{raw,_:=json.Marshal(value);return raw}
// --- typed phase activation ------------------------------------------------
// A progressing Process activates known pinned phase DAGs repeatedly. The
// declaration is validated here against the printed graph before any effect:
// exact phase coverage of the flattened Nodes, phase-local edges only,
// carried closed state with one exact type, an effect-free Function selector
// with a closed phase enum, and read-only retries limited to exact API
// Infer/Decision Nodes with typed API observation effects. No shape is
// inferred and no recurrence count is capped.

type progressionEndpoint struct {
	Node  string
	Field string
}

type progressionPhase struct {
	Key         string
	Nodes       []string
	StateInput  progressionEndpoint
	StateOutput progressionEndpoint
	NextOutput  *progressionEndpoint
}

type progressionDeclaration struct {
 Frontier *runtimeFrontier
 Regions []runtimeRegion
 Events []runtimeActivationEvent
 Emit func(progressionPhase,map[string]map[string]any)error
 SettledFailures []runtimeSettledFailureRoute
	Schema          string
	Initial         string
	Terminal        string
	Phases          []progressionPhase
	Continuation *continuationDeclaration
 Prelude         *runtimePrelude
	ReadOnlyRetries []string
}
type runtimePrelude struct {
	Nodes       []string
	StateOutput progressionEndpoint
}

// progressionExactKeys refuses any member outside the allowed set; the printed
// declaration is closed at every level.
func progressionExactKeys(m map[string]any, allowed ...string) bool {
	known := map[string]bool{}
	for _, key := range allowed {
		known[key] = true
	}
	for key := range m {
		if !known[key] {
			return false
		}
	}
	return true
}

func progressionEndpointFrom(raw any) (progressionEndpoint, bool) {
	m, ok := raw.(map[string]any)
	if !ok || !progressionExactKeys(m, "node", "field") {
		return progressionEndpoint{}, false
	}
	node, nodeOK := m["node"].(string)
	field, fieldOK := m["field"].(string)
	if !nodeOK || !fieldOK || node == "" || field == "" {
		return progressionEndpoint{}, false
	}
	return progressionEndpoint{Node: node, Field: field}, true
}

// progressionPortField returns one exact required typed port of the printed
// spec for an endpoint, or refuses.
func progressionPortField(specs map[string][]map[string]any, endpoint progressionEndpoint, side string) (map[string]any, error) {
	for _, field := range specs[endpoint.Node] {
		if field["name"] == endpoint.Field && field["side"] == side {
			if field["required"] != true {
				return nil, fmt.Errorf("progression endpoint must be a required port")
			}
			return field, nil
		}
	}
	return nil, fmt.Errorf("progression endpoint is absent from its printed spec")
}

// parseWorkerProgression validates the flattened declaration against the
// printed graph and refuses every unsafe shape before an activation runs.
func parseWorkerProgression(processDoc map[string]any, nodes []processNode, edges []processEdge, specs map[string][]map[string]any, kinds map[string]string) (*progressionDeclaration, error) {
	raw, present := processDoc["progression"]
	if !present || raw == nil {
		return nil, nil
	}
	doc, ok := raw.(map[string]any)
 if ok && (doc["schema"]=="praxis.process_frontier.v1" || doc["frontier"]!=nil) {return parseWorkerFrontier(processDoc,doc,nodes,edges,specs,kinds)}
 if ok {if _,regional:=doc["regions"];regional{return parseWorkerRegions(processDoc,doc,nodes,edges,specs,kinds)}}
	if !ok || !progressionExactKeys(doc, "schema", "initial", "terminal", "phases", "prelude", "read_only_retries", "continuation", "settled_failures") {
		return nil, fmt.Errorf("progression declaration shape")
	}
	declaration := &progressionDeclaration{}
 var continuationErr error;declaration.Continuation,continuationErr=parseContinuation(doc["continuation"]);if continuationErr!=nil{return nil,continuationErr}
	declaration.Schema, _ = doc["schema"].(string)
	declaration.Initial, _ = doc["initial"].(string)
	declaration.Terminal, _ = doc["terminal"].(string)
	if declaration.Schema != "" && declaration.Schema != "praxis.process_progression.v4" || declaration.Initial == "" || declaration.Terminal == "" {
		return nil, fmt.Errorf("progression declaration incomplete")
	}
	if loops, _ := processDoc["loops"].([]any); len(loops) > 0 {
		return nil, fmt.Errorf("progression cannot mix the generic loop exception")
	}
	phaseList, ok := doc["phases"].([]any)
	if !ok || len(phaseList) == 0 {
		return nil, fmt.Errorf("progression phases absent")
	}
	for _, item := range phaseList {
		m, ok := item.(map[string]any)
		if !ok || !progressionExactKeys(m, "key", "nodes", "state_input", "state_output", "next_output") {
			return nil, fmt.Errorf("progression phase shape")
		}
		phase := progressionPhase{}
		phase.Key, _ = m["key"].(string)
		if phase.Key == "" {
			return nil, fmt.Errorf("progression phase identity")
		}
		nodeList, ok := m["nodes"].([]any)
		if !ok || len(nodeList) == 0 {
			return nil, fmt.Errorf("progression phase Nodes absent")
		}
		for _, entry := range nodeList {
			key, ok := entry.(string)
			if !ok || key == "" {
				return nil, fmt.Errorf("progression phase Node identity")
			}
			phase.Nodes = append(phase.Nodes, key)
		}
		var okEndpoint bool
		if phase.StateInput, okEndpoint = progressionEndpointFrom(m["state_input"]); !okEndpoint {
			return nil, fmt.Errorf("progression state input shape")
		}
		if phase.StateOutput, okEndpoint = progressionEndpointFrom(m["state_output"]); !okEndpoint {
			return nil, fmt.Errorf("progression state output shape")
		}
		if rawNext, exists := m["next_output"]; exists && rawNext != nil {
			next, ok := progressionEndpointFrom(rawNext)
			if !ok {
				return nil, fmt.Errorf("progression selector shape")
			}
			phase.NextOutput = &next
		}
		declaration.Phases = append(declaration.Phases, phase)
	}
	if rawPrelude, exists := doc["prelude"]; exists && rawPrelude != nil {
		m, ok := rawPrelude.(map[string]any)
		if !ok || !progressionExactKeys(m, "nodes", "state_output") {
			return nil, fmt.Errorf("progression prelude shape")
		}
		prelude := &runtimePrelude{}
		nodeList, ok := m["nodes"].([]any)
		if !ok || len(nodeList) == 0 {
			return nil, fmt.Errorf("progression prelude Nodes absent")
		}
		for _, entry := range nodeList {
			key, ok := entry.(string)
			if !ok || key == "" {
				return nil, fmt.Errorf("progression prelude Node identity")
			}
			prelude.Nodes = append(prelude.Nodes, key)
		}
		endpoint, ok := progressionEndpointFrom(m["state_output"])
		if !ok {
			return nil, fmt.Errorf("progression prelude state output shape")
		}
		prelude.StateOutput = endpoint
		declaration.Prelude = prelude
	}
	if rawRetries, exists := doc["read_only_retries"]; exists && rawRetries != nil {
		list, ok := rawRetries.([]any)
		if !ok {
			return nil, fmt.Errorf("progression retry declaration shape")
		}
		for _, entry := range list {
			key, ok := entry.(string)
			if !ok || key == "" {
				return nil, fmt.Errorf("progression retry declaration shape")
			}
			declaration.ReadOnlyRetries = append(declaration.ReadOnlyRetries, key)
		}
	}

	known := map[string]processNode{}
	for _, node := range nodes {
		known[node.key] = node
	}
	// Effects come from each Node's pinned printed spec, never from a guess.
	effects := map[string][]any{}
	for key, node := range known {
		entry, ok := dispatch[node.reference()]
		if !ok {
			return nil, fmt.Errorf("progression Node dispatch absent")
		}
		document, err := specDocument(entry.spec)
		if err != nil {
			return nil, err
		}
		composition, _ := document["composition"].(map[string]any)
		list, _ := composition["effects"].([]any)
		effects[key] = list
	}
	phases := map[string]progressionPhase{}
	owner := map[string]string{}
	for _, phase := range declaration.Phases {
		if phases[phase.Key].Key != "" {
			return nil, fmt.Errorf("progression phase identity duplicated")
		}
		phases[phase.Key] = phase
		for _, key := range phase.Nodes {
			node, knownNode := known[key]
			if !knownNode || node.recoveryOnly || owner[key] != "" {
				return nil, fmt.Errorf("progression Node coverage mismatch")
			}
			owner[key] = phase.Key
		}
		if owner[phase.StateInput.Node] != phase.Key || owner[phase.StateOutput.Node] != phase.Key {
			return nil, fmt.Errorf("progression state endpoint outside its phase")
		}
	}
	if declaration.Prelude != nil {
		preludeOwner := map[string]bool{}
		for _, key := range declaration.Prelude.Nodes {
			node, knownNode := known[key]
			if !knownNode || node.recoveryOnly || owner[key] != "" || preludeOwner[key] {
				return nil, fmt.Errorf("progression prelude Node coverage mismatch")
			}
			preludeOwner[key] = true
			owner[key] = "prelude"
		}
		if !preludeOwner[declaration.Prelude.StateOutput.Node] {
			return nil, fmt.Errorf("progression prelude state output outside the opening DAG")
		}
	}
	if phases[declaration.Initial].Key == "" || phases[declaration.Terminal].Key == "" {
		return nil, fmt.Errorf("progression initial or terminal phase unknown")
	}
	for _, node := range nodes {
		if !node.recoveryOnly && owner[node.key] == "" {
			return nil, fmt.Errorf("progression Node is unassigned")
		}
	}
	for _, edge := range edges {
		if owner[edge.fromNode] == "" || owner[edge.fromNode] != owner[edge.toNode] {
			return nil, fmt.Errorf("progression cross-phase edge refused")
		}
	}
	retry := map[string]bool{}
	for _, key := range declaration.ReadOnlyRetries {
		if retry[key] || owner[key] == "" {
			return nil, fmt.Errorf("progression read-only retry declaration invalid")
		}
		if workerRecoveryTarget(processDoc, key) { return nil, fmt.Errorf("progression replay and reconciliation classifications conflict") }
		kind := kinds[key]
		if kind != "infer" && kind != "decision" {
			return nil, fmt.Errorf("progression read-only retry must be an API Infer or native Decision Node")
		}
		required := "infer.api"
		if kind == "decision" {
			required = "decision.api"
		}
		effect := ""
		if len(effects[key]) == 1 {
			effect, _ = effects[key][0].(string)
		}
		if effect != required {
			return nil, fmt.Errorf("progression read-only retry effect is not a typed API observation")
		}
		retry[key] = true
	}

	root, err := progressionPortField(specs, phases[declaration.Initial].StateInput, "input")
	if err != nil {
		return nil, err
	}
	rootType, _ := root["type"].(map[string]any)
	if rootType["type"] != "object" || rootType["additionalProperties"] != false {
		return nil, fmt.Errorf("progression state must be a closed typed object")
	}
	if _, err := contractschema.ParseMap(rootType, contractschema.ModeStrict); err != nil {
		return nil, err
	}
	if declaration.Prelude != nil {
		output, err := progressionPortField(specs, declaration.Prelude.StateOutput, "output")
		if err != nil {
			return nil, err
		}
		if !sameStateValue(root, output) {
			return nil, fmt.Errorf("progression prelude state type mismatch")
		}
		for _, key := range declaration.Prelude.Nodes {
			if kinds[key] == "function" {
				if len(effects[key]) != 0 {
					return nil, fmt.Errorf("progression prelude Function must be effect-free")
				}
				continue
			}
			if !retry[key] && !workerRecoveryTarget(processDoc, key) {
				return nil, fmt.Errorf("progression prelude API Node requires an exact read-only retry classification or pinned reconciliation")
			}
		}
	}
	sameState := func(field map[string]any) bool {
		for _, name := range []string{"type", "semantic", "inner", "requirements"} {
			left, _ := json.Marshal(root[name])
			right, _ := json.Marshal(field[name])
			if !bytes.Equal(left, right) {
				return false
			}
		}
		return true
	}
	for _, phase := range declaration.Phases {
		input, err := progressionPortField(specs, phase.StateInput, "input")
		if err != nil {
			return nil, err
		}
		output, err := progressionPortField(specs, phase.StateOutput, "output")
		if err != nil {
			return nil, err
		}
		if !sameState(input) || !sameState(output) {
			return nil, fmt.Errorf("progression state type mismatch")
		}
		for _, key := range phase.Nodes {
			if kinds[key] == "function" {
				if len(effects[key]) != 0 {
					return nil, fmt.Errorf("progression Function must be effect-free")
				}
				continue
			}
			if !retry[key] && !workerRecoveryTarget(processDoc, key) {
				return nil, fmt.Errorf("progression API Node requires an exact read-only retry classification or pinned reconciliation")
			}
		}
		if phase.Key == declaration.Terminal {
			if phase.NextOutput != nil {
				return nil, fmt.Errorf("terminal progression phase cannot select a next phase")
			}
			continue
		}
		if phase.NextOutput == nil || owner[phase.NextOutput.Node] != phase.Key || kinds[phase.NextOutput.Node] != "function" {
			return nil, fmt.Errorf("progression selector must be a pure Function inside its phase")
		}
		selector, err := progressionPortField(specs, *phase.NextOutput, "output")
		if err != nil {
			return nil, err
		}
		selectorType, _ := selector["type"].(map[string]any)
		members, ok := selectorType["enum"].([]any)
		if selectorType["type"] != "string" || !ok || len(members) == 0 {
			return nil, fmt.Errorf("progression selector requires a closed phase enum")
		}
		for _, member := range members {
			key, ok := member.(string)
			if !ok || phases[key].Key == "" {
				return nil, fmt.Errorf("progression selector names an unknown phase")
			}
		}
	}
	if err:=parseWorkerSettledFailures(doc,declaration,nodes,edges,specs,kinds,owner);err!=nil{return nil,err}
 return declaration, nil
}

// progressionActivationKey is the immutable identity of one Node activation:
// phase/<round>/<phase-key>/<static-node-key>. The static key still selects
// the pinned implementation, spec and credential binding.
// sameStateValue compares two printed spec fields for the exact closed state
// contract (type, semantic, inner, requirements).
func sameStateValue(root, field map[string]any) bool {
	for _, name := range []string{"type", "semantic", "inner", "requirements"} {
		left, _ := json.Marshal(root[name])
		right, _ := json.Marshal(field[name])
		if !bytes.Equal(left, right) {
			return false
		}
	}
	return true
}

func progressionEgressSelection(r *recoveryRun,all,active []processNode,readOnly map[string]bool,round int,phase string)(map[string]bool,error){
 selected:=map[string]bool{}
 for _,node:=range active{
  key:=progressionActivationKey(round,phase,node.key);if phase=="prelude"{key="prelude/"+node.key}
  checkpoint,err:=r.claim.Node(key);if err!=nil{return nil,err}
  if checkpoint!=nil && checkpoint.State=="settled"{continue}
  if checkpoint==nil || readOnly[node.key]{selected[node.key]=true;continue}
  kind,err:=specIdentityKind(dispatch[node.reference()].spec);if err!=nil{return nil,err};if kind=="function"{selected[node.key]=true;continue}
  if r.policy!=nil{for _,b:=range r.policy.Bindings{if recoveryKey(b.Target.Block,b.Target.Node)==node.key{if len(b.Reconciler.Block.Nodes)!=1{return nil,fmt.Errorf("exact private reconciliation Node required")};selected[recoveryKey(b.Reconciler.Key,b.Reconciler.Block.Nodes[0].Key)]=true}}}
 }
 return selected,nil
}
func knownNodeName(nodes []processNode,key string)string{for _,n:=range nodes{if n.key==key{return n.node}};return ""}
func knownNodeRef(nodes []processNode,key string)nodeRef{for _,n:=range nodes{if n.key==key{return n.reference()}};return nodeRef{}}
func progressionActivationKey(round int, phase, key string) string {
	return fmt.Sprintf("phase/%d/%s/%s", round, phase, key)
}

// runActivation executes one immutable activation inside the execution-owned
// checkpoint journal. A sealed activation is reusable only under its exact
// input identity; a changed input refuses. A started activation with no
// sealed outcome reruns only when it is an effect-free Function or an exact
// classified read-only API Node; the lost attempt keeps its unknown output and
// spend and the replacement is separately labeled.
func (r *recoveryRun) runActivation(nodes []processNode, edges []processEdge, specs map[string][]map[string]any, results map[string]map[string]any, key, activation string, bv boundView, handle egress.InvocationHandle, readOnly bool) (map[string]any, error) {
	inputs, err := nodeInputs(nodes, edges, results, key)
	if err != nil {
		return nil, &checkpointFailure{err}
	}
	// The activation request identity covers the intrinsic runtime inputs so
	// the portable verifier recomputes the same digest for the same event.
	injectRuntimeInputs(nodes, key, inputs)
	digest, err := recoveryInputDigest(inputs)
	if err != nil {
		return nil, &checkpointFailure{err}
	}
    replacement,err:=r.prepareReplacement(key,activation,inputs,specs[key],bv);if err!=nil{return nil,&checkpointFailure{err}}
	attempt := 1
	if r.claim != nil {
		checkpoint, err := r.claim.Node(activation)
		if err != nil {
			return nil, &checkpointFailure{err}
		}
		if checkpoint != nil {
			if checkpoint.InputDigest != digest {
				return nil, &checkpointFailure{fmt.Errorf("activation input identity changed")}
			}
			if checkpoint.State == "settled" {
				if checkpoint.Outcome == nil {
					return nil, &checkpointFailure{fmt.Errorf("activation outcome missing")}
				}
				var evidence map[string]any
				if len(checkpoint.Outcome.Receipts) > 0 {
					if err := decodeProcessJSON(checkpoint.Outcome.Receipts, &evidence); err != nil {
						return nil, &checkpointFailure{err}
					}
				}
				r.evidence.Store(activation, evidence)
				// Retained attempts are receipt-visible accounting, so they
				// are lowered to plain JSON values: a typed Go slice would
				// make the immutable receipt undigestible.
				attempts := []any{}
				for _, attempt := range checkpoint.Attempts {
                    // Typed failure is private journal custody; the existing
                    // receipt attempt contract retains error/accounting only.
                    attempt.Failure = nil
					raw, err := json.Marshal(attempt)
					if err != nil {
						return nil, &checkpointFailure{err}
					}
					value, err := valuecontract.Decode(raw)
					if err != nil {
						return nil, &checkpointFailure{err}
					}
					attempts = append(attempts, value)
				}
				origin := map[string]any{"origin": "restored", "request_digest": digest, "attempts": attempts}
                if len(checkpoint.Outcome.Resolution) > 0 { var resolution map[string]any; if err := decodeProcessJSON(checkpoint.Outcome.Resolution, &resolution); err != nil { return nil, &checkpointFailure{err} }; origin["resolution"] = resolution }
                r.origins.Store(activation, origin)
                if err:=r.retainReplacement(replacement,activation,true);err!=nil{return nil,&checkpointFailure{err}}
				if checkpoint.Outcome.Error != "" {
					return nil, restoreCheckpointFailure(checkpoint.Outcome)
				}
				var output map[string]any
				if err := decodeProcessJSON(checkpoint.Outcome.Outputs, &output); err != nil {
					return nil, &checkpointFailure{err}
				}
				if err := validateRecoveredOutputs(specs[key], key, output, bv); err != nil {
					return nil, &checkpointFailure{err}
				}
				return output, nil
			}
			if checkpoint.State != "started" {
				return nil, &checkpointFailure{fmt.Errorf("activation checkpoint state invalid")}
			}
            if replacement!=nil{if err:=r.retainReplacement(replacement,activation,false);err!=nil{return nil,&checkpointFailure{err}};return nil,&recoveryUnknown{message:"replacement activation is pending; original allowance cannot be replayed"}}
			pure := false
			for _, node := range nodes {
				if node.key == key {
					kind, err := specIdentityKind(dispatch[node.reference()].spec)
					if err != nil {
						return nil, &checkpointFailure{err}
					}
					pure = kind == "function"
				}
			}
			if !pure && !readOnly {
                out, err := r.reconcileActivation(nodes, specs, key, activation, inputs, digest, bv, handle)
                return out, err
			}
			// The interrupted attempt stays on the record: its output and
			// spend remain unknown, and the replacement is a new attempt.
			interrupted := fmt.Errorf("interrupted unsealed activation; output and spend remain unknown")
            rawFailure, err := json.Marshal(failureFields(interrupted)); if err != nil { return nil, &checkpointFailure{err} }
            if err := r.claim.RecordNodeAttempt(activation, digest, executionjournal.NodeAttempt{Error: interrupted.Error(), Failure: rawFailure, Receipts: json.RawMessage("{}")}); err != nil {
				return nil, &checkpointFailure{err}
			}
			attempt = len(checkpoint.Attempts) + 2
		}
        if replacement!=nil&&!replacement.fresh{if err:=r.retainReplacement(replacement,activation,false);err!=nil{return nil,&checkpointFailure{err}};return nil,&recoveryUnknown{message:"replacement reservation interrupted; no fresh effect lease"}}
		if err := r.claim.StartNode(activation, digest); err != nil {
			return nil, &checkpointFailure{err}
		}
	}
    if replacement!=nil{if err:=replacement.beforeEffect();err!=nil{return nil,&checkpointFailure{err}}}
	// Harness receipts belong to this immutable activation, even when several
	// jobs execute the same static Node concurrently.
	activationNodes:=append([]processNode(nil),nodes...);for i:=range activationNodes{if activationNodes[i].key==key{activationNodes[i].receiptKey=activation}}
	out, runErr := invokeRecoverySafe(activationNodes, edges, specs, results, key, bv, handle, nil)
	evidence, evidenceErr := executionEvidence(activation, handle)
	if evidenceErr != nil { return nil, &checkpointFailure{evidenceErr} }
	r.evidence.Store(activation, evidence)
	r.origins.Store(activation, map[string]any{"origin": "executed", "request_digest": digest, "attempt": strconv.Itoa(attempt)})
	if r.claim != nil {
		raw, err := json.Marshal(evidence)
		if err != nil {
			return nil, &checkpointFailure{err}
		}
		var rawFailure json.RawMessage
        if runErr != nil { rawFailure, err = json.Marshal(failureFields(runErr)); if err != nil { return nil, &checkpointFailure{err} } }
        if runErr != nil && !readOnly && uncertainRecoveryFailure(nodes, key, evidence) {
            raw, err := json.Marshal(evidence); if err != nil { return nil, &checkpointFailure{err} }
            if err := r.claim.RecordNodeAttempt(activation, digest, executionjournal.NodeAttempt{Error: runErr.Error(), Failure: rawFailure, Receipts: raw}); err != nil { return nil, &checkpointFailure{err} }
            if err:=r.retainReplacement(replacement,activation,false);err!=nil{return nil,&checkpointFailure{err}}
            return nil, &recoveryUnknown{message: "effect may have occurred; pinned reconciliation is required", failure: failureFields(runErr)}
        }
        if runErr != nil && readOnly && !settledRecoveryFailure(nodes, key, evidence) {
			if err := r.claim.RecordNodeAttempt(activation, digest, executionjournal.NodeAttempt{Error: runErr.Error(), Failure: rawFailure, Receipts: raw}); err != nil {
				return nil, &checkpointFailure{err}
			}
            if err:=r.retainReplacement(replacement,activation,false);err!=nil{return nil,&checkpointFailure{err}}
			fields := failureFields(runErr)
			fields["display"] = "read-only activation pending; restart continues with a new labeled attempt"
			return nil, &recoveryUnknown{message: "read-only activation pending; restart continues with a new labeled attempt", failure: fields}
		}
		outcome := executionjournal.NodeOutcome{Receipts: raw}
		if runErr != nil {
			outcome.Error = runErr.Error()
			outcome.Failure = rawFailure
		} else {
			outcome.Outputs, err = json.Marshal(out)
			if err != nil {
				return nil, &checkpointFailure{err}
			}
		}
		if err := r.claim.SettleNode(activation, digest, outcome); err != nil {
			return nil, &checkpointFailure{err}
		}
	}
    if err:=r.retainReplacement(replacement,activation,true);err!=nil{return nil,&checkpointFailure{err}}
	return out, runErr
}

// runWorkerProgression activates phases until the pinned selector reaches the
// terminal phase or a phase/checkpoint/configuration failure stops the run.
// Within a phase, independent Nodes run concurrently in topological levels;
// receipt order is deterministic. Each phase preparation creates fresh scoped
// API handles, so every activation owns its declared request allowance.


// progressionCancelled is the stable terminal refusal emitted when the
// execution journal already carries a durable cancellation. The worker stops
// dispatching phases, never prints a completion and never resurrects work.
type progressionCancelled struct{reason string}
func(e *progressionCancelled)Error()string{
 if e.reason==""{return "execution is cancelled; no further phases will dispatch"}
 return "execution is cancelled: "+e.reason+"; no further phases will dispatch"
}

// progressionTerminalCheck consults the execution journal's durable terminal
// facts. It compiles against every journal generation: a journal without the
// cancellation capability reports nothing and the legacy runtime is unchanged.
func progressionTerminalCheck(r *recoveryRun)error{
 if r==nil||r.claim==nil{return nil}
 if fact,ok:=any(r.claim).(interface{Cancelled()(bool,string,error)});ok{
  cancelled,reason,err:=fact.Cancelled()
  if err!=nil{return err}
  if cancelled{return &progressionCancelled{reason:reason}}
 }
 return nil
}

// runWorkerProgression selects the exact pinned progression form: activation
// events dispatch through the activation owner, regional declarations through
// the regional owner, and a flat declaration through the phase loop.
func runWorkerProgression(declaration *progressionDeclaration, nodes []processNode, edges []processEdge, specs map[string][]map[string]any, bv boundView, recovery *recoveryRun, comp string, boundary *processRun, f cliFlags, deployEnv string, results map[string]map[string]any, failures, unknowns map[string]string, typedFailures map[string]map[string]any, runtimeValues map[string]string, continuation *continuationRun) ([]map[string]any, error) {
 if err:=progressionTerminalCheck(recovery);err!=nil{return nil,err}
 if declaration.Frontier!=nil{return runWorkerFrontier(declaration,nodes,edges,specs,bv,recovery,comp,boundary,f,deployEnv,results,failures,unknowns,typedFailures,runtimeValues,continuation)}
 if len(declaration.Events)>0{return runWorkerActivations(declaration,nodes,edges,specs,bv,recovery,comp,boundary,f,deployEnv,results,failures,unknowns,typedFailures,runtimeValues)}
 if len(declaration.Regions)>0{return runWorkerRegions(declaration,nodes,edges,specs,bv,recovery,comp,boundary,f,deployEnv,results,failures,unknowns,typedFailures,runtimeValues)}
 return runWorkerRegionLoop(declaration,nodes,edges,specs,bv,recovery,comp,boundary,f,deployEnv,results,failures,unknowns,typedFailures,runtimeValues,"",continuation)
}
func runWorkerRegionLoop(declaration *progressionDeclaration, nodes []processNode, edges []processEdge, specs map[string][]map[string]any, bv boundView, recovery *recoveryRun, comp string, boundary *processRun, f cliFlags, deployEnv string, results map[string]map[string]any, failures, unknowns map[string]string, typedFailures map[string]map[string]any, runtimeValues map[string]string, region string, continuation *continuationRun) ([]map[string]any, error) {
	phases := map[string]progressionPhase{}
	for _, phase := range declaration.Phases {
		phases[phase.Key] = phase
	}
	readOnly := map[string]bool{}
	for _, key := range declaration.ReadOnlyRetries {
		readOnly[key] = true
	}
	records := []map[string]any{}
	activationOrdinal := 0
    recovery.runtimeValues = runtimeValues
    recovery.activationOrdinals = map[string]int{}
    recovery.activationNodes = nodes
	var state any
	if declaration.Prelude != nil {
		prelude := declaration.Prelude
		selected := map[string]bool{}
		for _, key := range prelude.Nodes {
			selected[key] = true
		}
		preludeNodes := make([]processNode, 0, len(prelude.Nodes))
		for _, node := range nodes {
			if !selected[node.key] {
				continue
			}
			activated := node
			activated.fills = map[string]any{}
			for name, value := range node.fills {
				activated.fills[name] = value
			}
			preludeNodes = append(preludeNodes, activated)
		}
		preludeEdges := make([]processEdge, 0, len(edges))
		for _, edge := range edges {
			if selected[edge.fromNode] && selected[edge.toNode] {
				preludeEdges = append(preludeEdges, edge)
			}
		}
		levels, err := topo(preludeNodes, preludeEdges)
		if err != nil {
			return records, err
		}
		selection,err:=progressionEgressSelection(recovery,nodes,preludeNodes,readOnly,0,"prelude");if err!=nil{return records,err}
 handles, err := prepareSelectedEgress(comp, boundary, nodes, selection, f, deployEnv)
		if err != nil {
			return records, err
		}
		recovery.activationHandles = handles
        local := map[string]map[string]any{}
		preludeFailed := false
		for _, level := range levels {
 if err:=continuation.selectMode(local,f);err!=nil{cancelEgressHandles(handles);return records,err};continuation.inject(preludeNodes)
			ordered := append([]string(nil), level...)
			sort.Strings(ordered)
			for _, key := range ordered {
				activationOrdinal++
                recovery.activationOrdinals[key] = activationOrdinal
				for i := range preludeNodes {
					if preludeNodes[i].key != key {
						continue
					}
					fills, err := runtimeFillsFor(preludeNodes[i], runtimeValues, activationOrdinal)
					if err != nil {
						cancelEgressHandles(handles)
						return records, err
					}
					preludeNodes[i].runtimeFills = fills
				}
			}
			snapshot := map[string]map[string]any{}
			for key, value := range local {
				snapshot[key] = value
			}
			type preludeOutcome struct {
				key string
				out map[string]any
				err error
			}
			ch := make(chan preludeOutcome, len(level))
			var wg sync.WaitGroup
			for _, key := range level {
				wg.Add(1)
				go func(key string) {
					defer wg.Done()
					if err:=progressionTerminalCheck(recovery);err!=nil{ch<-preludeOutcome{key:key,err:err};return}
					if err:=continuation.beforeActivation(region+"prelude/"+key,knownNodeRef(nodes,key));err!=nil{ch<-preludeOutcome{key:key,err:err};return}
 out, err := recovery.runActivation(preludeNodes, preludeEdges, specs, snapshot, key, region+"prelude/"+key, bv, handles[key], readOnly[key])
					ch <- preludeOutcome{key: key, out: out, err: err}
				}(key)
			}
			wg.Wait()
			close(ch)
			completed := make([]preludeOutcome, 0, len(level))
			for outcome := range ch {
				completed = append(completed, outcome)
			}
			if len(completed) != len(level) {
				cancelEgressHandles(handles)
				return records, fmt.Errorf("activation stopped without a reported outcome; inspect before retrying")
			}
			sort.Slice(completed, func(i, j int) bool { return completed[i].key < completed[j].key })
			for _, outcome := range completed {
				key := outcome.key
				activation := region+"prelude/" + key
				record := map[string]any{"activation": activation, "phase": "prelude", "round": "1", "node": key, "outputs": normJSON(outcome.out), "state": "succeeded"}
				if evidence, ok := recovery.evidence.Load(activation); ok {
					for name, value := range evidence.(map[string]any) {
						record[name] = value
					}
				}
				if origin, ok := recovery.origins.Load(activation); ok {
					record["execution"] = origin
				}
 attachSettledCheckpoints(record,recovery,activation)
 attachSettledCheckpoints(record,recovery,activation)
				if boundary != nil {
					record["source"] = boundary.sources[key]
					record["process"] = boundary.ref
				}
				if outcome.err != nil {
					var cancelled *progressionCancelled
					if errors.As(outcome.err,&cancelled) {cancelEgressHandles(handles);return records, cancelled}
					var fatal *checkpointFailure
                    if errors.As(outcome.err,&fatal) {
						cancelEgressHandles(handles)
						return records, fatal
					}
					record["state"] = "failed"
					fields := failureFields(outcome.err)
					record["error"] = fields["display"]
					record["failure"] = fields
					typedFailures[key] = fields
					var unknown *recoveryUnknown
                    if errors.As(outcome.err,&unknown) {
						record["state"] = "pending"
						unknowns[key] = fields["display"].(string)
					} else {
						failures[key] = fields["display"].(string)
					}
					preludeFailed = true
				} else {
					local[key] = outcome.out
					results[key] = outcome.out
				}
				records = append(records, record)
			}
			if preludeFailed {
				break
			}
		}
		cancelEgressHandles(handles)
		if preludeFailed {
			return records, nil
		}
		value, ok := local[prelude.StateOutput.Node][prelude.StateOutput.Field]
		if !ok {
			return records, fmt.Errorf("progression prelude state output is absent")
		}
		if err:=continuation.selectMode(local,f);err!=nil{return records,err}
 state = value
		field, err := progressionPortField(specs, prelude.StateOutput, "output")
		if err != nil {
			return records, err
		}
		if err := checkValue(field, state, nil); err != nil {
			return records, fmt.Errorf("progression prelude state: %v", err)
		}
	} else {
		initial := phases[declaration.Initial]
		for _, node := range nodes {
			if node.key == initial.StateInput.Node {
				state = node.fills[initial.StateInput.Field]
			}
		}
		if state == nil {
			return nil, fmt.Errorf("initial progression state input is absent")
		}
	}
	phaseKey := declaration.Initial
	for round := 1; ; round++ {
		// A cancellation recorded between phases stops every further dispatch:
		// the durable terminal fact is the evidence, and no completion receipt
		// is fabricated for the cancelled remainder.
		if err:=progressionTerminalCheck(recovery);err!=nil{return records,err}
		phaseStart:=len(records)
 phase, ok := phases[phaseKey]
		if !ok {
			return records, fmt.Errorf("unknown selected phase")
		}
		selected := map[string]bool{}
		for _, key := range phase.Nodes {
			selected[key] = true
		}
		phaseNodes := make([]processNode, 0, len(phase.Nodes))
		for _, node := range nodes {
			if !selected[node.key] {
				continue
			}
			activated := node
			activated.fills = map[string]any{}
			for name, value := range node.fills {
				activated.fills[name] = value
			}
			if node.key == phase.StateInput.Node {
				activated.fills[phase.StateInput.Field] = state
			}
			phaseNodes = append(phaseNodes, activated)
		}
		phaseEdges := make([]processEdge, 0, len(edges))
		for _, edge := range edges {
			if selected[edge.fromNode] && selected[edge.toNode] {
				phaseEdges = append(phaseEdges, edge)
			}
		}
		levels, err := topo(phaseNodes, phaseEdges)
		if err != nil {
			return records, err
		}
		// Fresh scoped handles per phase preparation: every activation has
		// its own declared request ceiling and every handle is released at
		// the phase boundary.
		selection,err:=progressionEgressSelection(recovery,nodes,phaseNodes,readOnly,round,phase.Key);if err!=nil{return records,err}
 handles, err := prepareSelectedEgress(comp, boundary, nodes, selection, f, deployEnv)
		if err != nil {
			return records, err
		}
		recovery.activationHandles = handles
        local := map[string]map[string]any{}
        recovery.activationKeys=map[string]string{};for _,n:=range phaseNodes{recovery.activationKeys[n.key]=region+progressionActivationKey(round,phase.Key,n.key)}
		phaseFailed := false
		for _, level := range levels {
			// The activation sequence is the deterministic 1-based ordinal of
			// each activation in execution order: assigned here, before any
			// execution, so restored and resumed activations keep the exact
			// same values they had when their receipts were first written.
			ordered := append([]string(nil), level...)
			sort.Strings(ordered)
			for _, key := range ordered {
				activationOrdinal++
                recovery.activationOrdinals[key] = activationOrdinal
				for i := range phaseNodes {
					if phaseNodes[i].key != key {
						continue
					}
					fills, err := runtimeFillsFor(phaseNodes[i], runtimeValues, activationOrdinal)
					if err != nil {
						cancelEgressHandles(handles)
						return records, err
					}
					phaseNodes[i].runtimeFills = fills
				}
			}
            if err:=resolveActivationContexts(phaseNodes,level,recovery,phase.Key,round,f);err!=nil{cancelEgressHandles(handles);return records,err}
if err:=resolveSettledCheckpoints(phaseNodes,level,recovery);err!=nil{cancelEgressHandles(handles);return records,err}
            if err:=recovery.replacement.resolveContexts(phaseNodes,local,recovery.activationKeys,level);err!=nil{cancelEgressHandles(handles);return records,err}
            if err:=recovery.replacement.resolveCustodies(phaseNodes,recovery.activationKeys,level);err!=nil{cancelEgressHandles(handles);return records,err}
			snapshot := map[string]map[string]any{}
			for key, value := range local {
				snapshot[key] = value
			}
			type activationOutcome struct {
				key string
				out map[string]any
				err error
			}
			ch := make(chan activationOutcome, len(level))
			var wg sync.WaitGroup
			for _, key := range level {
				wg.Add(1)
				go func(key string) {
					defer wg.Done()
					activation := region+progressionActivationKey(round, phase.Key, key)
					if err:=progressionTerminalCheck(recovery);err!=nil{ch<-activationOutcome{key:key,err:err};return}
					if err:=continuation.beforeActivation(activation,knownNodeRef(nodes,key));err!=nil{ch<-activationOutcome{key:key,err:err};return}
 out, err := recovery.runActivation(phaseNodes, phaseEdges, specs, snapshot, key, activation, bv, handles[key], readOnly[key])
					ch <- activationOutcome{key: key, out: out, err: err}
				}(key)
			}
			wg.Wait()
			close(ch)
			completed := make([]activationOutcome, 0, len(level))
			for outcome := range ch {
				completed = append(completed, outcome)
			}
			if len(completed) != len(level) {
				cancelEgressHandles(handles)
				return records, fmt.Errorf("activation stopped without a reported outcome; inspect before retrying")
			}
			sort.Slice(completed, func(i, j int) bool { return completed[i].key < completed[j].key })
			for _, outcome := range completed {
				key := outcome.key
				activation := region+progressionActivationKey(round, phase.Key, key)
				record := map[string]any{"activation": activation, "phase": phase.Key, "round": strconv.Itoa(round), "node": key, "outputs": normJSON(outcome.out), "state": "succeeded"}
				if evidence, ok := recovery.evidence.Load(activation); ok {
					for name, value := range evidence.(map[string]any) {
						record[name] = value
					}
				}
				if origin, ok := recovery.origins.Load(activation); ok {
					record["execution"] = origin
                    for _,candidate:=range phaseNodes{if candidate.key==key{for _,source:=range candidate.runtime{if source=="settled_failures"{var capsule map[string]any;if err:=decodeProcessJSON([]byte(runtimeValues[source]),&capsule);err!=nil{return records,err};origin.(map[string]any)["settled_failure_capsule"]=capsule}}}}
				}
 attachSettledCheckpoints(record,recovery,activation)
				if boundary != nil {
					record["source"] = boundary.sources[key]
					record["process"] = boundary.ref
				}
				if outcome.err != nil {
					var cancelled *progressionCancelled
					if errors.As(outcome.err,&cancelled) {cancelEgressHandles(handles);return records, cancelled}
					var fatal *checkpointFailure
                    if errors.As(outcome.err,&fatal) {
						cancelEgressHandles(handles)
						return records, fatal
					}
					record["state"] = "failed"
					fields := failureFields(outcome.err)
					record["error"] = fields["display"]
					record["failure"] = fields
					typedFailures[key] = fields
					var unknown *recoveryUnknown
                    if errors.As(outcome.err,&unknown) {
						record["state"] = "pending"
						unknowns[key] = fields["display"].(string)
					} else {
						failures[key] = fields["display"].(string)
					}
					phaseFailed = true
				} else {
					local[key] = outcome.out
					results[key] = outcome.out
				}
				records = append(records, record)
			}
			if phaseFailed {
				break
			}
		}
		cancelEgressHandles(handles)
		if phaseFailed {
            handler,capsule,handled,err:=settledFailureHandoff(declaration,recovery,phase.Key,strconv.Itoa(round),state,records[phaseStart:],f)
            if err!=nil{return records,err}
            if !handled{return records,nil}
            raw,err:=json.Marshal(capsule);if err!=nil{return records,err};runtimeValues["settled_failures"]=string(raw)
            for _,record:=range records[phaseStart:]{if record["state"]=="failed"{key,_:=record["node"].(string);delete(failures,key)}}
            phaseKey=handler;continue
        }
		nextState, ok := local[phase.StateOutput.Node][phase.StateOutput.Field]
		if !ok {
			return records, fmt.Errorf("progression carried state is absent from the phase output")
		}
		state = nextState
		field, err := progressionPortField(specs, phase.StateOutput, "output")
		if err != nil {
			return records, err
		}
		if err := checkValue(field, state, nil); err != nil {
			return records, fmt.Errorf("progression carried state: %v", err)
		}
 if declaration.Emit != nil { if err := declaration.Emit(phase,local); err != nil {return records,err} }
		if phase.Key == declaration.Terminal {
 if len(unknowns)>0{return records,fmt.Errorf("unresolved activations cannot enter a fresh continuation round")}
 resume,err:=continuation.finish(phase.Key,round,records,local);if err!=nil{return records,err};if resume!=""{if phases[resume].Key==""{return records,fmt.Errorf("continuation resume phase absent")};phaseKey=resume;continue}
			return records, nil
		}
		next, ok := local[phase.NextOutput.Node][phase.NextOutput.Field].(string)
		if !ok || phases[next].Key == "" {
			return records, fmt.Errorf("progression selector emitted an unknown phase")
		}
		phaseKey = next
	}
}

// workerRecoveryTarget reads only the pinned Process policy. It grants no
// retry permission; runActivation uses the read-only reconciler instead.
func workerRecoveryTarget(processDoc map[string]any, key string) bool {
 process, _ := processDoc["process"].(map[string]any)
 policy, _ := process["policy"].(map[string]any)
 recovery, _ := policy["recovery"].(map[string]any)
 bindings, _ := recovery["bindings"].([]any)
 for _, raw := range bindings { binding, _ := raw.(map[string]any); target, _ := binding["target"].(map[string]any); block, _ := target["block"].(string); node, _ := target["node"].(string); if recoveryKey(block,node) == key { return true } }
 return false
}

// reconcileActivation isolates the existing reconciler's checkpoint view to
// this immutable activation. Private readback Nodes remain exact pinned code
// and receive the original inputs; no phase or implementation is synthesized.
func (r *recoveryRun) reconcileActivation(nodes []processNode, specs map[string][]map[string]any, key, activation string, inputs map[string]any, digest string, bv boundView, handle egress.InvocationHandle) (map[string]any, error) {
 if r.policy == nil { return nil, &recoveryUnknown{message:"activation needs configured recovery", failure:failureFields(nil)} }
 return r.reconcileAt(r.activationNodes, nil, specs, nil, key, activation, inputs, digest, bv, r.activationHandles)
}

type readyOutcome struct { key string; out map[string]any; err error }

func runReadyNodes(keys []string, edges []processEdge,
 run func(string,map[string]map[string]any)(map[string]any,error),
 settle func(readyOutcome)error) error {
 members:=map[string]bool{}
 waiting:=map[string]map[string]bool{}
 children:=map[string]map[string]bool{}
 for _,key:=range keys {
  if members[key] {return fmt.Errorf("readiness Node duplicated")}
  members[key]=true;waiting[key]=map[string]bool{};children[key]=map[string]bool{}
 }
 for _,edge:=range edges {
  if !members[edge.fromNode]||!members[edge.toNode] {return fmt.Errorf("readiness edge outside graph")}
  waiting[edge.toNode][edge.fromNode]=true;children[edge.fromNode][edge.toNode]=true
 }
 // Validate before starting effects, including disconnected cyclic branches.
 pending:=map[string]int{};queue:=[]string{}
 for key:=range members {pending[key]=len(waiting[key]);if pending[key]==0{queue=append(queue,key)}}
 visited:=0
 for len(queue)>0 {key:=queue[0];queue=queue[1:];visited++;for child:=range children[key]{pending[child]--;if pending[child]==0{queue=append(queue,child)}}}
 if visited!=len(keys){return fmt.Errorf("readiness graph has a cycle")}
 outputs:=map[string]map[string]any{}
 launched:=map[string]bool{}
 completed:=make(chan readyOutcome,len(keys))
 active:=0
 var fatal error
 launch:=func(key string){
  snapshot:=map[string]map[string]any{};for k,v:=range outputs{snapshot[k]=v}
  launched[key]=true;active++
  go func(){
   outcome:=readyOutcome{key:key};returned:=false
   defer func(){
    if value:=recover();value!=nil{outcome.out=nil;outcome.err=fmt.Errorf("adapter panic: %v",value)
    }else if !returned{outcome.out=nil;outcome.err=fmt.Errorf("node execution stopped without a reported outcome; inspect before retrying")}
    completed<-outcome
   }()
   outcome.out,outcome.err=run(key,snapshot);returned=true
  }()
 }
 roots:=[]string{};for _,key:=range keys{if len(waiting[key])==0{roots=append(roots,key)}};sort.Strings(roots)
 for _,key:=range roots{launch(key)}
 for active>0 {
  outcome:=<-completed;active--
  if err:=settle(outcome);err!=nil&&fatal==nil{fatal=err}
  if outcome.err!=nil||fatal!=nil{continue}
  outputs[outcome.key]=outcome.out
  ready:=[]string{}
  for child:=range children[outcome.key]{
   delete(waiting[child],outcome.key)
   if len(waiting[child])==0&&!launched[child]{ready=append(ready,child)}
  }
  sort.Strings(ready);for _,key:=range ready{launch(key)}
 }
 return fatal
}

type runtimeRegion struct {Key string;TriggerOnly bool;Loop *progressionDeclaration;Nodes []processNode;Edges []processEdge}
type regionalFailure struct{}
func(regionalFailure)Error()string{return "region did not satisfy its outcome"}


func workerRegionDocKeys(doc map[string]any)bool{
 for name,value:=range doc{
  switch name{
  case "schema","regions","events":
  case "initial","terminal":
   if value!=nil&&value!=""{return false}
  case "phases":
   if value!=nil{if list,ok:=value.([]any);!ok||len(list)!=0{return false}}
  case "prelude","read_only_retries","settled_failures","continuation":
   if value!=nil{return false}
  default:
   return false
  }
 }
 return true
}
func parseWorkerRegions(processDoc,doc map[string]any,nodes []processNode,edges []processEdge,specs map[string][]map[string]any,kinds map[string]string)(*progressionDeclaration,error){
 if !workerRegionDocKeys(doc)||doc["schema"]!=nil&&doc["schema"]!=""&&doc["schema"]!="praxis.process_progression.v4"{return nil,fmt.Errorf("regional progression shape")}
 raw,ok:=doc["regions"].([]any);if !ok||len(raw)==0{return nil,fmt.Errorf("regions absent")}
 declaration:=&progressionDeclaration{Schema:"praxis.process_progression.v4"}
 owners:=map[string]string{};keys:=map[string]bool{}
 for _,item:=range raw{
  row,ok:=item.(map[string]any);if !ok||!progressionExactKeys(row,"key","loop","trigger_only"){return nil,fmt.Errorf("region shape")}
  key,_:=row["key"].(string);loop,ok:=row["loop"].(map[string]any)
  if key==""||keys[key]||!ok{return nil,fmt.Errorf("region identity")};keys[key]=true
  if _,nested:=loop["regions"];nested{return nil,fmt.Errorf("nested runtime regions refused")}
  if _,present:=loop["continuation"];present{return nil,fmt.Errorf("continuation requires scoped region contract")}
  membership:=map[string]bool{}
  lists:=[]any{}
  phases,_:=loop["phases"].([]any);for _,phase:=range phases{p,_:=phase.(map[string]any);lists=append(lists,p["nodes"])}
  if prelude,ok:=loop["prelude"].(map[string]any);ok{lists=append(lists,prelude["nodes"])}
  for _,list:=range lists{entries,_:=list.([]any);for _,entry:=range entries{name,ok:=entry.(string);if !ok||name==""||owners[name]!=""{return nil,fmt.Errorf("region Node reused")};owners[name]=key;membership[name]=true}}
  // Private recovery Nodes belong to the region of their exact target.
  process,_:=processDoc["process"].(map[string]any);policy,_:=process["policy"].(map[string]any);recovery,_:=policy["recovery"].(map[string]any);bindings,_:=recovery["bindings"].([]any)
  for _,rawBinding:=range bindings{binding,_:=rawBinding.(map[string]any);target,_:=binding["target"].(map[string]any);block,_:=target["block"].(string);node,_:=target["node"].(string);if !membership[recoveryKey(block,node)]{continue};probe,_:=binding["reconciler"].(map[string]any);probeKey,_:=probe["key"].(string);body,_:=probe["block"].(map[string]any);probeNodes,_:=body["nodes"].([]any);for _,rawNode:=range probeNodes{n,_:=rawNode.(map[string]any);name,_:=n["key"].(string);membership[recoveryKey(probeKey,name)]=true}}
  trigger,_:=row["trigger_only"].(bool);if value,present:=row["trigger_only"];present&&value!=true{return nil,fmt.Errorf("activation template discriminator")};region:=runtimeRegion{Key:key,TriggerOnly:trigger};for _,node:=range nodes{if membership[node.key]{region.Nodes=append(region.Nodes,node)}}
  for _,edge:=range edges{if membership[edge.fromNode]&&membership[edge.toNode]{region.Edges=append(region.Edges,edge)}}
  projected:=map[string]any{};for name,value:=range processDoc{projected[name]=value};projected["progression"]=loop
  parsed,err:=parseWorkerProgression(projected,region.Nodes,region.Edges,specs,kinds);if err!=nil{return nil,err};region.Loop=parsed
  declaration.Regions=append(declaration.Regions,region)
 }
 for _,node:=range nodes{if !node.recoveryOnly&&owners[node.key]==""{return nil,fmt.Errorf("regional Node unassigned")}}
 _,_,err:=workerRegionGraph(declaration,edges);if err!=nil{return nil,err}
 if err=parseWorkerActivationEvents(doc,declaration,specs,kinds);err!=nil{return nil,err}
 return declaration,nil
}

func workerRegionGraph(declaration *progressionDeclaration,edges []processEdge)(map[string]string,[]processEdge,error){
 owners:=map[string]string{};terminal:=map[string]bool{};opening:=map[string]bool{}
 for _,region:=range declaration.Regions{
  for _,node:=range region.Nodes{if !node.recoveryOnly{owners[node.key]=region.Key}}
  for _,phase:=range region.Loop.Phases{
   if phase.Key==region.Loop.Terminal{for _,key:=range phase.Nodes{terminal[key]=true}}
   if region.Loop.Prelude==nil&&phase.Key==region.Loop.Initial{opening[phase.StateInput.Node]=true}
  }
  if region.Loop.Prelude!=nil{for _,key:=range region.Loop.Prelude.Nodes{opening[key]=true}}
 }
 regionEdges:=[]processEdge{}
 for _,edge:=range edges{
  from,to:=owners[edge.fromNode],owners[edge.toNode]
  if from==to{continue}
  if from==""||to==""||!terminal[edge.fromNode]||!opening[edge.toNode]{return nil,nil,fmt.Errorf("regional edge must bind terminal to opening")}
  regionEdges=append(regionEdges,processEdge{fromNode:from,toNode:to})
 }
 // Cycle validation here happens before any region starts an effect.
 graphNodes:=[]processNode{};for _,region:=range declaration.Regions{graphNodes=append(graphNodes,processNode{key:region.Key})}
 if _,err:=topo(graphNodes,regionEdges);err!=nil{return nil,nil,err}
 return owners,regionEdges,nil
}

func runWorkerRegions(declaration *progressionDeclaration,nodes []processNode,edges []processEdge,specs map[string][]map[string]any,bv boundView,recovery *recoveryRun,comp string,boundary *processRun,f cliFlags,deployEnv string,results map[string]map[string]any,failures,unknowns map[string]string,typedFailures map[string]map[string]any,runtimeValues map[string]string)([]map[string]any,error){
 owners,regionEdges,err:=workerRegionGraph(declaration,edges);if err!=nil{return nil,err}
 regions:=map[string]runtimeRegion{};keys:=[]string{};graphNodes:=[]processNode{}
 for _,region:=range declaration.Regions{regions[region.Key]=region;keys=append(keys,region.Key);graphNodes=append(graphNodes,processNode{key:region.Key})}
 records:=map[string][]map[string]any{}
 err=runReadyNodes(keys,regionEdges,func(key string,available map[string]map[string]any)(map[string]any,error){
  region:=regions[key];activated:=make([]processNode,len(region.Nodes));copy(activated,region.Nodes)
  for i:=range activated{fills:=map[string]any{};for name,value:=range activated[i].fills{fills[name]=value};activated[i].fills=fills}
  for _,edge:=range edges{if owners[edge.toNode]!=key||owners[edge.fromNode]==key{continue};parent:=available[owners[edge.fromNode]];prior,_:=parent["results"].(map[string]map[string]any);value,present:=prior[edge.fromNode][edge.fromField];if !present{return nil,fmt.Errorf("region prerequisite output absent")};for i:=range activated{if activated[i].key==edge.toNode{activated[i].fills[edge.toField]=value}}}
  local:=map[string]map[string]any{};failed:=map[string]string{};uncertain:=map[string]string{};typed:=map[string]map[string]any{}
  ownedRecovery:=newRecoveryRun(boundary,recovery.claim)
  intrinsic:=map[string]string{};for name,value:=range runtimeValues{intrinsic[name]=value}
  if intrinsic["invocation_id"]!=""{digest,ok:=canonDigest(map[string]any{"root":intrinsic["invocation_id"],"region":key},"praxis.region_invocation_id.v1");if !ok{return nil,fmt.Errorf("region invocation identity")};intrinsic["invocation_id"]=digest}
  chain,runErr:=runWorkerRegionLoop(region.Loop,activated,region.Edges,specs,bv,ownedRecovery,comp,boundary,f,deployEnv,local,failed,uncertain,typed,intrinsic,"region/"+key+"/",nil)
  out:=map[string]any{"results":local,"records":chain,"failures":failed,"unknowns":uncertain,"typed":typed}
  if runErr!=nil{return out,runErr};if len(failed)+len(uncertain)>0{return out,regionalFailure{}}
  return out,nil
 },func(outcome readyOutcome)error{
  if outcome.out!=nil{
   local,_:=outcome.out["results"].(map[string]map[string]any);for key,value:=range local{results[key]=value}
   records[outcome.key],_=outcome.out["records"].([]map[string]any)
   failed,_:=outcome.out["failures"].(map[string]string);for key,value:=range failed{failures[key]=value}
   uncertain,_:=outcome.out["unknowns"].(map[string]string);for key,value:=range uncertain{unknowns[key]=value}
   typed,_:=outcome.out["typed"].(map[string]map[string]any);for key,value:=range typed{typedFailures[key]=value}
  }
  if _,known:=outcome.err.(regionalFailure);known{return nil};return outcome.err
 })
 levels,_:=topo(graphNodes,regionEdges);chain:=[]map[string]any{}
 for _,level:=range levels{sort.Strings(level);for _,key:=range level{chain=append(chain,records[key]...)}}
 return chain,err
}

type runtimeActivationEvent struct{From progressionEndpoint;Target string}
type activationEmission struct{Parent string;Mapping runtimeActivationEvent;Items []any}
type activationResult struct{Key string;Region string;Parent string;Records []map[string]any;Results map[string]map[string]any;Failures,Unknowns map[string]string;Typed map[string]map[string]any;Err error}
func parseWorkerActivationEvents(doc map[string]any,d *progressionDeclaration,specs map[string][]map[string]any,kinds map[string]string)error{
 regions:=map[string]runtimeRegion{};owner:=map[string]string{};for _,region:=range d.Regions{regions[region.Key]=region;for _,node:=range region.Nodes{owner[node.key]=region.Key}}
 list,exists:=doc["events"];if !exists{for _,region:=range d.Regions{if region.TriggerOnly{return fmt.Errorf("activation template lacks events")}};return nil}
 raw,ok:=list.([]any);if !ok||len(raw)==0{return fmt.Errorf("activation events absent")};seen:=map[progressionEndpoint]bool{}
 for _,item:=range raw{row,ok:=item.(map[string]any);if !ok||!progressionExactKeys(row,"from","target"){return fmt.Errorf("activation mapping shape")};from,ok:=progressionEndpointFrom(row["from"]);target,_:=row["target"].(string);region,known:=regions[target];if !ok||!known||!region.TriggerOnly||region.Loop.Prelude!=nil||owner[from.Node]==""||kinds[from.Node]!="function"||seen[from]{return fmt.Errorf("activation mapping authority")};seen[from]=true
  source,err:=progressionPortField(specs,from,"output");if err!=nil{return err};var opening progressionPhase;for _,phase:=range region.Loop.Phases{if phase.Key==region.Loop.Initial{opening=phase}}
  sink,err:=progressionPortField(specs,opening.StateInput,"input");if err!=nil{return err}
  expected:=map[string]any{"type":"array","items":map[string]any{"type":"object","additionalProperties":false,"required":[]string{"work_id","work_revision","contribution_id","state"},"properties":map[string]any{"work_id":map[string]any{"type":"string"},"work_revision":map[string]any{"type":"integer"},"contribution_id":map[string]any{"type":"string"},"state":sink["type"]}}}
  a,_:=json.Marshal(source["type"]);b,_:=json.Marshal(expected);if !bytes.Equal(a,b){return fmt.Errorf("activation event state differs")};d.Events=append(d.Events,runtimeActivationEvent{From:from,Target:target})
 }
 return nil
}
func runWorkerActivations(d *progressionDeclaration,nodes []processNode,edges []processEdge,specs map[string][]map[string]any,bv boundView,recovery *recoveryRun,comp string,boundary *processRun,f cliFlags,deployEnv string,results map[string]map[string]any,failures,unknowns map[string]string,typedFailures map[string]map[string]any,runtimeValues map[string]string)([]map[string]any,error){
 owners,cross,err:=workerRegionGraph(d,edges);if err!=nil{return nil,err}
 regions:=map[string]runtimeRegion{};roots:=[]string{};for _,region:=range d.Regions{regions[region.Key]=region;if !region.TriggerOnly{roots=append(roots,region.Key)}};sort.Strings(roots)
 for _,edge:=range cross{if regions[edge.fromNode].TriggerOnly||regions[edge.toNode].TriggerOnly{return nil,fmt.Errorf("activation cannot use static region edge")}}
 emissions:=make(chan activationEmission);completed:=make(chan activationResult);stop:=make(chan struct{});defer close(stop)
 started:=map[string]string{};done:=map[string]activationResult{};parents:=map[string]string{};active:=0
 launch:=func(region runtimeRegion,key,parent string,initial any)error{
  pin,ok:=canonDigest(initial,"praxis.activation_initial_state.v1");if !ok{return fmt.Errorf("activation input canonicalization")}
  if old,seen:=started[key];seen{if old!=pin{return fmt.Errorf("activation identity conflicts with prior inputs")};return nil};started[key]=pin;parents[key]=parent;active++
  activated:=make([]processNode,len(region.Nodes));copy(activated,region.Nodes)
  for i:=range activated{activated[i].fills=map[string]any{};for name,value:=range region.Nodes[i].fills{activated[i].fills[name]=value}}
  if region.TriggerOnly{for _,phase:=range region.Loop.Phases{if phase.Key==region.Loop.Initial{for i:=range activated{if activated[i].key==phase.StateInput.Node{activated[i].fills[phase.StateInput.Field]=initial}}}}}else{
   for _,edge:=range edges{if owners[edge.toNode]!=region.Key||owners[edge.fromNode]==region.Key{continue};prior:=done[owners[edge.fromNode]].Results;value,ok:=prior[edge.fromNode][edge.fromField];if !ok{return fmt.Errorf("activation prerequisite absent")};for i:=range activated{if activated[i].key==edge.toNode{activated[i].fills[edge.toField]=value}}}
  }
  loop:=*region.Loop
  loop.Emit=func(phase progressionPhase,out map[string]map[string]any)error{member:=map[string]bool{};for _,node:=range phase.Nodes{member[node]=true};for _,mapping:=range d.Events{if !member[mapping.From.Node]{continue};value,present:=out[mapping.From.Node][mapping.From.Field];if !present{return fmt.Errorf("declared activation output absent")};raw,err:=json.Marshal(value);if err!=nil{return err};var items []any;decoder:=json.NewDecoder(bytes.NewReader(raw));decoder.UseNumber();if err=decoder.Decode(&items);err!=nil{return err};select{case emissions<-activationEmission{Parent:key,Mapping:mapping,Items:items}:case <-stop:return fmt.Errorf("activation owner stopped")}};return nil}
  intrinsic:=map[string]string{};for name,value:=range runtimeValues{intrinsic[name]=value};if intrinsic["invocation_id"]!=""{pin,ok:=canonDigest(map[string]any{"root":intrinsic["invocation_id"],"region":key},"praxis.region_invocation_id.v1");if !ok{return fmt.Errorf("activation execution identity")};intrinsic["invocation_id"]=pin}
  go func(){local:=map[string]map[string]any{};failed:=map[string]string{};uncertain:=map[string]string{};typed:=map[string]map[string]any{};owned:=newRecoveryRun(boundary,recovery.claim)
   chain,runErr:=runWorkerRegionLoop(&loop,activated,region.Edges,specs,bv,owned,comp,boundary,f,deployEnv,local,failed,uncertain,typed,intrinsic,"region/"+key+"/",nil)
   select{case completed<-activationResult{Key:key,Region:region.Key,Parent:parent,Records:chain,Results:local,Failures:failed,Unknowns:uncertain,Typed:typed,Err:runErr}:case <-stop:}
  }();return nil
 }
 startRoots:=func()error{for _,key:=range roots{if _,seen:=started[key];seen{continue};ready:=true;for _,edge:=range cross{if edge.toNode==key{if _,ok:=done[edge.fromNode];!ok{ready=false}}};if ready{if err:=launch(regions[key],key,"",nil);err!=nil{return err}}};return nil}
 if err=startRoots();err!=nil{return nil,err};var runErr error
 for active>0{
  select{
  case event:=<-emissions:
   for _,item:=range event.Items{request,ok:=item.(map[string]any);if !ok||!progressionExactKeys(request,"work_id","work_revision","contribution_id","state"){return nil,fmt.Errorf("activation request shape")};work,_:=request["work_id"].(string);contribution,_:=request["contribution_id"].(string);revision,ok:=request["work_revision"].(json.Number);token,exact:=exactNumberToken(string(revision));number,e:=strconv.ParseInt(token,10,64);if work==""||contribution==""||!ok||!exact||e!=nil||number<1{return nil,fmt.Errorf("activation identity incomplete")}
    pin,ok:=canonDigest(map[string]any{"execution":runtimeValues["invocation_id"],"region":event.Mapping.Target,"work_id":work,"work_revision":revision,"contribution_id":contribution},"praxis.widget_activation.v3");if !ok{return nil,fmt.Errorf("activation identity")};key:=event.Mapping.Target+"/"+pin
    if err:=launch(regions[event.Mapping.Target],key,event.Parent,request["state"]);err!=nil{return nil,err}
   }
  case outcome:=<-completed:
   active--;done[outcome.Key]=outcome;if outcome.Err!=nil&&runErr==nil{runErr=outcome.Err}
   for node,value:=range outcome.Results{if !regions[outcome.Region].TriggerOnly{results[node]=value}}
   for node,value:=range outcome.Failures{failures[outcome.Key+"/"+node]=value};for node,value:=range outcome.Unknowns{unknowns[outcome.Key+"/"+node]=value};for node,value:=range outcome.Typed{typedFailures[outcome.Key+"/"+node]=value}
   if err=startRoots();err!=nil{return nil,err}
  }
 }
 // Canonical receipt order follows causality, independent of completion order.
 chain:=[]map[string]any{};ordered:=map[string]bool{}
 for len(ordered)<len(done){level:=[]string{};for key:=range done{if ordered[key]{continue};parent:=parents[key];if parent!=""&&!ordered[parent]{continue};ready:=true;if parent==""{for _,edge:=range cross{if edge.toNode==key&&!ordered[edge.fromNode]{ready=false}}};if ready{level=append(level,key)}};if len(level)==0{return nil,fmt.Errorf("activation receipt causality")};sort.Strings(level);for _,key:=range level{ordered[key]=true;chain=append(chain,done[key].Records...)}}
 return chain,runErr
}

func validateRootBindings(boundary *processRun,progression *progressionDeclaration,nodes []processNode,f cliFlags)error{
 boundary.allowedBindings=map[string]bool{}
 for _,node:=range nodes{entry:=dispatch[node.reference()];if entry.infer!=nil{boundary.allowedBindings[node.key]=true;continue};if entry.scoped==nil&&entry.scopedConfig==nil{continue};doc,err:=specDocument(entry.spec);if err!=nil{return err};for _,dimension:=range doc["dimensions"].([]any){value,_:=dimension.(map[string]any);if value["name"]!="egress"{continue};raw,_:=json.Marshal(value["contents"]);policy,err:=egress.Parse(raw);if err!=nil{return err};for _,op:=range policy.Operations{key:=node.key;if len(policy.Operations)>1{key+="/"+op.ID};boundary.allowedBindings[key]=true}}}
 if f.egressBindings==""{return nil};raw,err:=os.ReadFile(f.egressBindings);if err!=nil{return err};var bindings map[string]egress.Binding;decoder:=json.NewDecoder(bytes.NewReader(raw));decoder.DisallowUnknownFields();if err=decoder.Decode(&bindings);err!=nil{return err};for name:=range bindings{if !boundary.allowedBindings[name]{return fmt.Errorf("binding names undeclared capability")}};return nil
}

// --- pill scope and late binding -------------------------------------------
// Frozen contracts pill.address.v1 and pill.slot.v1 (atlas joints) are
// mirrored here, never imported: the printed worker stays stdlib-only and
// shares no code with the atlas module.
//
// A pill is a blank inside the execution scope. The worker resolves it only
// from its own scope: invocation inputs, node fills, and completed node
// outputs. Each bind is verified against the declared late slot (type and
// bounds) plus the pin and the receipt, and every late bind leaves a bind
// receipt. Anything unresolvable in scope is refusal, always: the worker
// never reaches outside its own scope and never calls back to any harness.
//
// Deployment declares the late world: pill_slots ("node.field" to a
// pill.slot.v1), pill_pins (target ref to revision digest), and
// extension_points (tool-input names the harness may pass that the compiled
// inputs never saw, each with its endpoint and accepted type). Anything
// undeclared is refused exactly as before.
//
// Field-path selection is the bundled datatools SelectionStep traversal
// (pillCompileSteps + SelectSteps), not a parallel resolver.

type pillTarget struct {
	Ref            string
	RevisionDigest string
}

type pillReceipt struct {
	Digest string
	URI    string
}

type pillAddress struct {
	Target    pillTarget
	Port      string
	FieldPath string
	Kind      string
	Receipt   *pillReceipt
}

type pillSlotType struct {
	Name   string
	Format string
}

// pillBoundCitation cites one deployment bound entry by key plus the digest
// of that entry, mirroring the compiler intake. Resolution matches the key
// against the deployment bounds section, then the digest against the entry.
type pillBoundCitation struct {
	Key    string
	Digest string
}

type pillSlot struct {
	Binding  string
	Type     pillSlotType
	Bounds   []pillBoundCitation
	Required bool
}

// pillSlotTypeNames is the closed Forge subset type vocabulary a slot may
// name, mirroring the compiler intake. Colloquial aliases are not slot
// names and refuse at parse time.
var pillSlotTypeNames = map[string]bool{
	"object": true, "array": true, "string": true, "boolean": true,
	"integer": true, "number": true, "null": true, "scalar": true,
}

// pillScope is the whole execution scope a pill may resolve from: the
// invocation inputs, every node's fills, the completed node outputs, and
// the pinned revisions. Lookup never touches disk, environment, network,
// subprocesses, or any harness: pure map walks over this scope.
type pillScope struct {
	inv     map[string]any
	fills   map[string]map[string]any
	outputs map[string]map[string]any
	pins    map[string]string
}

type pillExtensionPoint struct {
	name  string
	node  string
	field string
	typ   map[string]any
}

var pillSlots = map[string]map[string]any{}
var pillPins = map[string]string{}
var pillInvInputs = map[string]any{}

// pillBoundEntries indexes the deployment bounds section by bound key so a
// late bind can resolve slot citations. It is loaded once in initPillScope
// alongside the slots and pins; citation digests are matched at bind time.
var pillBoundEntries = map[string]map[string]any{}

// pillBindRecords holds one bind receipt per late bind by node key. Nodes
// bind on their own goroutine; appends take the mutex so concurrent binds
// cannot tear a node's list.
var pillBindRecords sync.Map
var pillBindMu sync.Mutex

func isPillAddress(value any) bool {
	m, ok := value.(map[string]any)
	if !ok {
		return false
	}
	schema, _ := m["schema"].(string)
	return schema == "pill.address.v1"
}

func pillDigestShape(s string) bool {
	// Forge-side pill artifacts use bare lowercase hex (the canonjson
	// Digest / NodeSpecDigest form shared with pins and admission
	// digests). The sha256:-prefixed leaf-world form never governs here
	// and is refused as unverifiable.
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func pillNameShape(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9'
		if !ok {
			return false
		}
	}
	return true
}

func pillFieldPathShape(s string) bool {
	if s == "" {
		return false
	}
	for _, segment := range strings.Split(s, ".") {
		if !pillNameShape(segment) {
			return false
		}
	}
	return true
}

// parsePillAddress enforces pill.address.v1: a pinned target, shaped port
// and field path, and the kind/receipt rule (value pills carry an upstream
// receipt, composition pills must not).
func parsePillAddress(raw map[string]any) (pillAddress, error) {
	var addr pillAddress
	if schema, _ := raw["schema"].(string); schema != "pill.address.v1" {
		return addr, fmt.Errorf("pill address schema")
	}
	target, ok := raw["target"].(map[string]any)
	if !ok {
		return addr, fmt.Errorf("pill address target")
	}
	ref, _ := target["ref"].(string)
	digest, _ := target["revision_digest"].(string)
	if strings.TrimSpace(ref) == "" {
		return addr, fmt.Errorf("pill address ref")
	}
	if !pillDigestShape(digest) {
		return addr, fmt.Errorf("pill address revision")
	}
	addr.Target = pillTarget{Ref: ref, RevisionDigest: digest}
	port, _ := raw["port"].(string)
	if !pillNameShape(port) {
		return addr, fmt.Errorf("pill address port")
	}
	addr.Port = port
	path, _ := raw["field_path"].(string)
	if !pillFieldPathShape(path) {
		return addr, fmt.Errorf("pill address field")
	}
	addr.FieldPath = path
	kind, _ := raw["kind"].(string)
	switch kind {
	case "value":
		receipt, ok := raw["receipt"].(map[string]any)
		if !ok {
			return addr, fmt.Errorf("pill value needs a receipt")
		}
		rd, _ := receipt["digest"].(string)
		if !pillDigestShape(rd) {
			return addr, fmt.Errorf("pill value receipt unverifiable")
		}
		uri, _ := receipt["uri"].(string)
		addr.Receipt = &pillReceipt{Digest: rd, URI: uri}
	case "composition":
		if _, present := raw["receipt"]; present {
			return addr, fmt.Errorf("pill composition must not carry a receipt")
		}
	default:
		return addr, fmt.Errorf("pill address kind")
	}
	addr.Kind = kind
	return addr, nil
}

// pillFloat and pillInt read JSON numbers (float64 after decoding, or
// json.Number where a decoder kept precision).
func pillFloat(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func pillInt(value any) (int, bool) {
	f, ok := pillFloat(value)
	if !ok || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// parsePillSlot enforces pill.slot.v1: a closed binding, a Forge subset
// type name with an optional format qualifier, a present required flag,
// and optional bound citations. Bounds, when present, are a
// {"citations": [{"key", "digest"}]} object citing deployment bound
// entries; each key is non-empty and each digest is bare hex. Citation
// resolution against bound entries happens at bind time, never here.
func parsePillSlot(raw map[string]any) (pillSlot, error) {
	var slot pillSlot
	if schema, _ := raw["schema"].(string); schema != "pill.slot.v1" {
		return slot, fmt.Errorf("pill slot schema")
	}
	binding, _ := raw["binding"].(string)
	if binding != "early" && binding != "late" {
		return slot, fmt.Errorf("pill slot binding")
	}
	slot.Binding = binding
	typ, ok := raw["type"].(map[string]any)
	if !ok {
		return slot, fmt.Errorf("pill slot type")
	}
	name, _ := typ["name"].(string)
	if !pillSlotTypeNames[name] {
		return slot, fmt.Errorf("pill slot type")
	}
	format, _ := typ["format"].(string)
	slot.Type = pillSlotType{Name: name, Format: format}
	required, present := raw["required"].(bool)
	if !present {
		return slot, fmt.Errorf("pill slot required")
	}
	slot.Required = required
	bounds, hasBounds := raw["bounds"].(map[string]any)
	if !hasBounds {
		if raw["bounds"] != nil {
			return slot, fmt.Errorf("pill slot bounds")
		}
		return slot, nil
	}
	list, ok := bounds["citations"].([]any)
	if !ok {
		return slot, fmt.Errorf("pill slot bounds")
	}
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return slot, fmt.Errorf("pill slot bounds")
		}
		key, _ := entry["key"].(string)
		digest, _ := entry["digest"].(string)
		if key == "" || !pillDigestShape(digest) {
			return slot, fmt.Errorf("pill slot bounds")
		}
		slot.Bounds = append(slot.Bounds, pillBoundCitation{Key: key, Digest: digest})
	}
	return slot, nil
}

// Bundled datatools field selection -----------------------------------------
// SelectionStep, SelectSteps, and selectSteps below are the datatools
// field-selection machinery verbatim (praxis-work/go/datatools/selection.go).
// The printed worker is stdlib-only and cannot import the datatools module,
// so the worker carries this exact source instead of a renamed port. Do not
// fork or rename it: pill field paths compile to property steps and execute
// through the bundled SelectSteps entry, so the worker resolves exactly
// what datatools resolves.

// SelectionStep is one datatools field-selection step: a property step reads
// one named field of an object and an each step maps the remainder over one
// collection without flattening it. The shape is tagless because backtick
// struct tags cannot appear inside this template literal; tags are never
// read here since steps are built in memory and never marshaled.
type SelectionStep struct {
	Kind string
	Name string
}

// pillCompileSteps compiles one validated dotted field path into datatools
// property SelectionSteps, one per segment. The pill.address.v1 field_path
// shape admits name segments only, never indices, so pill paths always
// compile to property steps.
func pillCompileSteps(path string) []SelectionStep {
	segments := strings.Split(path, ".")
	steps := make([]SelectionStep, 0, len(segments))
	for _, segment := range segments {
		steps = append(steps, SelectionStep{Kind: "property", Name: segment})
	}
	return steps
}

// SelectSteps executes the bundled datatools traversal over decoded JSON:
// property steps read object fields, each steps map over arrays. A missing
// property is absent, never an error; a step applied to the wrong kind is
// unresolvable. Compiled pill paths hold property steps only, so the each
// branch never fires on a pill bind; it stays so this remains the datatools
// traversal verbatim.
func SelectSteps(value any, steps []SelectionStep) (any, bool, error) {
	return selectSteps(value, steps)
}

func selectSteps(value any, steps []SelectionStep) (any, bool, error) {
	if len(steps) == 0 {
		return value, true, nil
	}
	step := steps[0]
	switch step.Kind {
	case "property":
		obj, ok := value.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("property selection requires an object")
		}
		child, ok := obj[step.Name]
		if !ok {
			return nil, false, nil
		}
		return selectSteps(child, steps[1:])
	case "each":
		if step.Name != "" {
			return nil, false, fmt.Errorf("each cannot name a property")
		}
		items, ok := value.([]any)
		if !ok {
			return nil, false, fmt.Errorf("each selection requires an array")
		}
		out := make([]any, 0, len(items))
		for i, item := range items {
			v, present, err := selectSteps(item, steps[1:])
			if err != nil {
				return nil, false, fmt.Errorf("item %d: %w", i, err)
			}
			if !present {
				return nil, false, fmt.Errorf("item %d: field absent; choose explicit missing-value handling", i)
			}
			out = append(out, v)
		}
		return out, true, nil
	default:
		return nil, false, fmt.Errorf("unknown selection step")
	}
}

// lookup resolves a pill address against the execution scope only. The
// target ref selects the record: "input" reads the invocation inputs, any
// other ref reads the completed outputs of that node first, then its
// fills. The pin must be declared and must match before anything else is
// read. Missing pins, revision drift, unknown refs, and missing values are
// refusal: unresolvable in scope never reaches further.
func (s *pillScope) lookup(addr pillAddress) (any, error) {
	want, pinned := s.pins[addr.Target.Ref]
	if !pinned || want != addr.Target.RevisionDigest {
		return nil, fmt.Errorf("pill pin unverifiable for %q", addr.Target.Ref)
	}
	var record any
	if addr.Target.Ref == "input" {
		record = s.inv
	} else if done, ok := s.outputs[addr.Target.Ref]; ok {
		record = done
	} else if fills, ok := s.fills[addr.Target.Ref]; ok {
		record = fills
	} else {
		return nil, fmt.Errorf("pill target %q not in execution scope", addr.Target.Ref)
	}
	// Field selection runs through the bundled datatools SelectionStep
	// traversal: the dotted field path compiles to property steps and
	// executes via SelectSteps. Absent values and wrong-kind steps alike
	// refuse as unresolvable in scope, exactly as before.
	value, present, err := SelectSteps(record, pillCompileSteps(addr.FieldPath))
	if err != nil || !present {
		return nil, fmt.Errorf("pill unresolvable in scope: %s.%s", addr.Target.Ref, addr.FieldPath)
	}
	return value, nil
}

// checkPillType enforces the slot type name against a decoded JSON value.
// Names are the closed Forge subset vocabulary validated at parse time;
// scalar admits any JSON scalar and null admits only null.
func checkPillType(name string, value any) error {
	switch name {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("pill type: want string")
		}
	case "number":
		if _, ok := pillFloat(value); !ok {
			return fmt.Errorf("pill type: want number")
		}
	case "integer":
		if _, ok := pillInt(value); !ok {
			return fmt.Errorf("pill type: want integer")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("pill type: want boolean")
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("pill type: want object")
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("pill type: want array")
		}
	case "null":
		if value != nil {
			return fmt.Errorf("pill type: want null")
		}
	case "scalar":
		switch value.(type) {
		case string, bool:
		default:
			if _, ok := pillFloat(value); !ok {
				return fmt.Errorf("pill type: want scalar")
			}
		}
	default:
		return fmt.Errorf("pill type: unknown name %q", name)
	}
	return nil
}

// checkPillBounds resolves every bound citation against the deployment
// bounds section and enforces it. Resolution matches by key, then by
// digest: an unknown key or a digest drift refuses, exactly like field
// bound refs. Only the documented count and range kinds apply here,
// enforced through the valuecontract bound machinery already bundled in
// the worker; any other kind refuses as inadmissible for pill slots. A
// bound that cannot apply to the value kind is a mismatch, never a pass.
func checkPillBounds(citations []pillBoundCitation, value any) error {
	for _, citation := range citations {
		entry, ok := pillBoundEntries[citation.Key]
		if !ok {
			return fmt.Errorf("pill bound %q unresolved", citation.Key)
		}
		digest, ok := canonDigest(entry, "praxis.bound.v1")
		if !ok || digest != citation.Digest {
			return fmt.Errorf("pill bound %q drift", citation.Key)
		}
		kind, _ := entry["kind"].(string)
		if kind != "count" && kind != "range" {
			return fmt.Errorf("pill bounds: unknown bound kind %q", kind)
		}
		params, _ := entry["params"].(map[string]any)
		subject, _ := entry["subject"].(string)
		measurement := "value"
		if strings.HasPrefix(subject, "length(") {
			measurement = "length"
		}
		if err := valuecontract.CheckBound(kind, measurement, params, value); err != nil {
			return err
		}
	}
	return nil
}

// pillSlotFor returns the declared slot for one node field, if any.
func pillSlotFor(nodeKey, field string) (map[string]any, bool) {
	slot, ok := pillSlots[nodeKey+"."+field]
	return slot, ok
}

// pillAppendRecord files one bind receipt under its node key.
func pillAppendRecord(nodeKey string, record map[string]any) {
	pillBindMu.Lock()
	defer pillBindMu.Unlock()
	var list []any
	if raw, ok := pillBindRecords.Load(nodeKey); ok {
		list, _ = raw.([]any)
	}
	list = append(list, record)
	pillBindRecords.Store(nodeKey, list)
}

// lateBindPill binds one pill blank at execution time: parse the address,
// hold it to the field it addresses, require a declared late slot, resolve
// strictly in scope, verify type and bounds plus pin and receipt, and file
// a bind receipt carrying the field identity, the value digest, and the
// check result. Every failure mode is refusal.
func lateBindPill(nodeKey, field string, raw any, scope *pillScope) (any, error) {
	addrMap, ok := raw.(map[string]any)
	if !ok || !isPillAddress(raw) {
		return nil, fmt.Errorf("pill blank shape")
	}
	addr, err := parsePillAddress(addrMap)
	if err != nil {
		return nil, err
	}
	if addr.Port != field {
		return nil, fmt.Errorf("pill port %q does not address field %q", addr.Port, field)
	}
	slotRaw, declared := pillSlotFor(nodeKey, field)
	if !declared {
		return nil, fmt.Errorf("pill blank without declared late slot")
	}
	slot, err := parsePillSlot(slotRaw)
	if err != nil {
		return nil, err
	}
	if slot.Binding != "late" {
		return nil, fmt.Errorf("pill slot binding %q is not late", slot.Binding)
	}
	value, err := scope.lookup(addr)
	if err != nil {
		return nil, err
	}
	if isPillAddress(value) {
		return nil, fmt.Errorf("pill resolves to another pill")
	}
	if err := checkPillType(slot.Type.Name, value); err != nil {
		return nil, err
	}
	if err := checkPillBounds(slot.Bounds, value); err != nil {
		return nil, err
	}
	digest, ok := canonDigest(normJSON(value), "praxis.pill.value.v1")
	if !ok {
		return nil, fmt.Errorf("pill value undigestible")
	}
	record := map[string]any{
		"node": nodeKey, "field": field,
		"ref": addr.Target.Ref, "port": addr.Port, "field_path": addr.FieldPath,
		"kind": addr.Kind, "check": "ok", "value_digest": digest,
	}
	if addr.Receipt != nil {
		record["receipt_digest"] = addr.Receipt.Digest
	}
	pillAppendRecord(nodeKey, record)
	return value, nil
}

// pillScopeFor assembles the execution scope for one bind: the invocation
// inputs, every node's fills, the completed outputs, and the pins. Fills
// and outputs are read-only here; nothing is fetched from anywhere else.
func pillScopeFor(nodes []processNode, snap map[string]map[string]any) *pillScope {
	fills := map[string]map[string]any{}
	for _, n := range nodes {
		fills[n.key] = n.fills
	}
	return &pillScope{inv: pillInvInputs, fills: fills, outputs: snap, pins: pillPins}
}

// resolvePillInputs late-binds every pill blank in one node's inputs
// before any other input validation runs, in field order for determinism.
// Concrete values pass through untouched.
func resolvePillInputs(key string, nodes []processNode, inputs map[string]any, snap map[string]map[string]any) error {
	blanks := []string{}
	for name, value := range inputs {
		if isPillAddress(value) {
			blanks = append(blanks, name)
		}
	}
	sort.Strings(blanks)
	if len(blanks) == 0 {
		return nil
	}
	scope := pillScopeFor(nodes, snap)
	for _, name := range blanks {
		value, err := lateBindPill(key, name, inputs[name], scope)
		if err != nil {
			return fmt.Errorf("input %s: %v", name, err)
		}
		inputs[name] = value
	}
	return nil
}

// findPillExtension returns the declared extension point for one
// never-seen tool input, or nil when the harness passed an undeclared name.
func findPillExtension(dep *deployment, name string) *pillExtensionPoint {
	if dep == nil {
		return nil
	}
	for i := range dep.extensions {
		if dep.extensions[i].name == name {
			return &dep.extensions[i]
		}
	}
	return nil
}

// initPillScope loads the deployment-declared late world into the worker
// globals and validates every declaration against the printed graph: slots
// name a known node and one of its input fields, extension points name a
// known node without colliding with an edge. Invocation inputs ride the
// process boundary when one exists.
func initPillScope(dep *deployment, nodes []processNode, edges []processEdge, specs map[string][]map[string]any, boundary *processRun) error {
	pillSlots = map[string]map[string]any{}
	pillPins = map[string]string{}
	pillInvInputs = map[string]any{}
	pillBoundEntries = map[string]map[string]any{}
	if boundary != nil {
		for name, value := range boundary.invInputs {
			pillInvInputs[name] = value
		}
	}
	if dep == nil {
		return nil
	}
	for _, e := range dep.bounds {
		key, _ := e["key"].(string)
		if key == "" {
			return fmt.Errorf("pill bound key")
		}
		pillBoundEntries[key] = e
	}
	known := map[string]bool{}
	for _, n := range nodes {
		known[n.key] = true
	}
	// Extension-fed fields are open slots too: the late binder enforces
	// the slot even though no spec field checks the value.
	extFields := map[string]bool{}
	if dep != nil {
		for _, ext := range dep.extensions {
			extFields[ext.node+"."+ext.field] = true
		}
	}
	for key, slot := range dep.pillSlots {
		node, field, ok := strings.Cut(key, ".")
		if !ok || !known[node] {
			return fmt.Errorf("pill slot %s: unknown node", key)
		}
		input := extFields[key]
		for _, f := range specs[node] {
			name, _ := f["name"].(string)
			side, _ := f["side"].(string)
			if side == "input" && name == field {
				input = true
			}
		}
		if !input {
			return fmt.Errorf("pill slot %s: unknown input", key)
		}
		pillSlots[key] = slot
	}
	for ref, digest := range dep.pillPins {
		pillPins[ref] = digest
	}
	for _, ext := range dep.extensions {
		if !known[ext.node] {
			return fmt.Errorf("extension point %s: unknown node", ext.name)
		}
		for _, e := range edges {
			if e.toNode == ext.node && e.toField == ext.field {
				return fmt.Errorf("extension point %s conflicts with an edge", ext.name)
			}
		}
	}
	return nil
}

type continuationDeclaration struct{}
type continuationRun struct{status map[string]any}
func parseContinuation(raw any)(*continuationDeclaration,error){if raw!=nil{return nil,fmt.Errorf("v4 owner requires conditional case Blocks for cross-execution state")};return nil,nil}
func beginContinuation(d *continuationDeclaration,b *processRun,f cliFlags,k string,j *executionjournal.Journal)(*continuationRun,error){if d!=nil{return nil,fmt.Errorf("legacy generation continuation is not a v4 capability")};return nil,nil}
func(r *continuationRun)close(){}
func(r *continuationRun)inject(nodes []processNode){}
func(r *continuationRun)beforeActivation(activation string,node nodeRef)error{return nil}
func(r *continuationRun)selectMode(local map[string]map[string]any,f cliFlags)error{return nil}
func(r *continuationRun)publish(records []map[string]any,local map[string]map[string]any)error{return nil}
func(r *continuationRun)finish(phase string,round int,records []map[string]any,local map[string]map[string]any)(string,error){return "",nil}
func(r *continuationRun)isPending()bool{return false}
func(r *continuationRun)recordPendingDelivery(response []byte,private string,failed bool)error{return fmt.Errorf("versioned journal owner has no generation pending-delivery capability")}
func(r *continuationRun)evidence()any{return nil}

// Replacement custody is a canonical v1-invocation capability. A v4 journal
// owner prints without it: these no-op bindings keep the shared worker
// template coherent while refusing nothing the journal runtime can provide.
type receiptReplacementNoop struct{fresh bool}
func(r *receiptReplacementNoop)resolveContexts([]processNode,map[string]map[string]any,map[string]string,[]string)error{return nil}
func(r *receiptReplacementNoop)resolveCustodies([]processNode,map[string]string,[]string)error{return nil}
func(r *recoveryRun)prepareReplacement(key,activation string,inputs map[string]any,fields []map[string]any,bv boundView)(*receiptReplacementNoop,error){return nil,nil}
func(r *recoveryRun)retainReplacement(a *receiptReplacementNoop,activation string,sealed bool)error{return nil}
func(a *receiptReplacementNoop)beforeEffect()error{return nil}
func replacementRuntimeDecode(raw []byte,out any) error {
 d:=json.NewDecoder(bytes.NewReader(raw));d.UseNumber();d.DisallowUnknownFields()
 if err:=d.Decode(out);err!=nil{return err}
 rest:=new(json.RawMessage);if err:=d.Decode(rest);err==nil{return fmt.Errorf("trailing JSON value")}
 return nil
}

type runtimeSettledFailureRoute struct{Target string;Codes []string;HandlerPhase string;Capsule progressionEndpoint}
func settledFailureSchema()map[string]any{var out map[string]any;_ = decodeProcessJSON([]byte(`{"type":"object","additionalProperties":false,"properties":{"schema":{"type":"string","enum":["praxis.process_settled_failures.v1"]},"scope":{"type":"string"},"event_id":{"type":"string"},"payload_sha256":{"type":"string"},"phase":{"type":"string"},"round":{"type":"string"},"incoming_state_sha256":{"type":"string"},"activations":{"type":"array","items":{"type":"object","additionalProperties":false,"properties":{"node":{"type":"string"},"activation":{"type":"string"},"checkpoint_json":{"type":"string"}},"required":["node","activation","checkpoint_json"]}}},"required":["schema","scope","event_id","payload_sha256","phase","round","incoming_state_sha256","activations"]}`),&out);return out}
func parseWorkerSettledFailures(doc map[string]any,d *progressionDeclaration,nodes []processNode,edges []processEdge,specs map[string][]map[string]any,kinds,owners map[string]string)error{
 raw,exists:=doc["settled_failures"];if exists{list,ok:=raw.([]any);if !ok{return fmt.Errorf("settled failure declarations must be an array")}
 for _,item:=range list{m,ok:=item.(map[string]any);if !ok||!progressionExactKeys(m,"target","codes","handler_phase","capsule"){return fmt.Errorf("settled failure route shape")};r:=runtimeSettledFailureRoute{};r.Target,_=m["target"].(string);r.HandlerPhase,_=m["handler_phase"].(string);var good bool;r.Capsule,good=progressionEndpointFrom(m["capsule"]);if !good{return fmt.Errorf("settled failure capsule endpoint")};codes,ok:=m["codes"].([]any);if !ok||len(codes)==0{return fmt.Errorf("settled failure codes")};seen:=map[string]bool{};for _,raw:=range codes{code,ok:=raw.(string);if !ok||strings.TrimSpace(code)==""||strings.TrimSpace(code)!=code||code=="*"||code=="runtime.failure.unknown"||seen[code]{return fmt.Errorf("settled failure code")};seen[code]=true;r.Codes=append(r.Codes,code)};d.SettledFailures=append(d.SettledFailures,r)} }
 targets:=map[string]bool{};capsules:=map[progressionEndpoint]bool{}
 for _,r:=range d.SettledFailures{
  if owners[r.Target]==""||owners[r.Target]=="prelude"||owners[r.Target]==r.HandlerPhase||r.HandlerPhase==d.Initial||owners[r.Capsule.Node]!=r.HandlerPhase||targets[r.Target]{return fmt.Errorf("settled failure route identity")};targets[r.Target]=true
  field,err:=progressionPortField(specs,r.Capsule,"input");if err!=nil{return err};if kinds[r.Capsule.Node]!="function"{return fmt.Errorf("settled failure capsule consumer must be pure")};a,_:=json.Marshal(field["type"]);b,_:=json.Marshal(settledFailureSchema());if !bytes.Equal(a,b){return fmt.Errorf("settled failure capsule type")}
  found:=false;for _,n:=range nodes{if n.key==r.Capsule.Node{if n.runtime[r.Capsule.Field]!="settled_failures"{return fmt.Errorf("settled failure runtime source")};if _,ok:=n.fills[r.Capsule.Field];ok{return fmt.Errorf("settled failure capsule filled")};found=true}}
  if !found{return fmt.Errorf("settled failure capsule absent")};for _,e:=range edges{if e.toNode==r.Capsule.Node&&e.toField==r.Capsule.Field{return fmt.Errorf("settled failure capsule edge")}}
  for _,p:=range d.Phases{if p.Key==r.HandlerPhase{for _,key:=range p.Nodes{if kinds[key]!="function"{return fmt.Errorf("settled failure handler must be pure")};for _,n:=range nodes{if n.key==key{doc,err:=specDocument(dispatch[n.reference()].spec);if err!=nil{return err};composition,_:=doc["composition"].(map[string]any);effects,_:=composition["effects"].([]any);if len(effects)!=0{return fmt.Errorf("settled failure handler Function has effects")}}}}};if p.NextOutput!=nil{selector,err:=progressionPortField(specs,*p.NextOutput,"output");if err!=nil{return err};typ,_:=selector["type"].(map[string]any);members,_:=typ["enum"].([]any);for _,m:=range members{if m==r.HandlerPhase{return fmt.Errorf("ordinary selector cannot enter failure handler")}}}}
  for _,phase:=range d.Phases{if phase.Key==r.HandlerPhase{if !workerFailureAncestor(edges,r.Capsule.Node,phase.StateInput.Node)||!workerFailureAncestor(edges,phase.StateOutput.Node,r.Capsule.Node){return fmt.Errorf("settled failure state ancestry")};if phase.NextOutput!=nil&&!workerFailureAncestor(edges,phase.NextOutput.Node,r.Capsule.Node){return fmt.Errorf("settled failure selector ancestry")}}}
  reachedTerminal:=false;seenPhase:=map[string]bool{};queuePhase:=[]string{r.HandlerPhase};for len(queuePhase)>0{key:=queuePhase[0];queuePhase=queuePhase[1:];if key==d.Terminal{reachedTerminal=true;break};if seenPhase[key]{continue};seenPhase[key]=true;for _,phase:=range d.Phases{if phase.Key==key&&phase.NextOutput!=nil{field,err:=progressionPortField(specs,*phase.NextOutput,"output");if err!=nil{return err};typ,_:=field["type"].(map[string]any);members,_:=typ["enum"].([]any);for _,m:=range members{if next,ok:=m.(string);ok{queuePhase=append(queuePhase,next)}}}}};if !reachedTerminal{return fmt.Errorf("settled failure handler cannot reach terminal")}
  capsules[r.Capsule]=true
 }
 for _,n:=range nodes{for field,source:=range n.runtime{if source=="settled_failures"&&!capsules[progressionEndpoint{Node:n.key,Field:field}]{return fmt.Errorf("unowned settled failure runtime")}}}
 return nil
}
func workerFailureAncestor(edges []processEdge,sink,source string)bool{if sink==source{return true};seen:=map[string]bool{};queue:=[]string{sink};for len(queue)>0{node:=queue[0];queue=queue[1:];for _,e:=range edges{if e.toNode==node{if e.fromNode==source{return true};if !seen[e.fromNode]{seen[e.fromNode]=true;queue=append(queue,e.fromNode)}}}};return false}
func settledFailureHandoff(d *progressionDeclaration,r *recoveryRun,phase,round string,state any,records []map[string]any,f cliFlags)(string,map[string]any,bool,error){
 if r==nil||r.claim==nil{return "",nil,false,fmt.Errorf("settled failure requires exact journal claim")}
 handler:="";matched:=false
 for _,record:=range records{
  if record["state"]=="pending"{return "",nil,false,nil}
  if record["state"]!="failed"{continue}
  key,_:=record["node"].(string);failure,_:=record["failure"].(map[string]any);code,_:=failure["code"].(string)
  found:=false
  for _,route:=range d.SettledFailures{if route.Target!=key{continue};for _,c:=range route.Codes{if code==c{if handler!=""&&handler!=route.HandlerPhase{return "",nil,false,fmt.Errorf("simultaneous failures have conflicting handler phases")};handler=route.HandlerPhase;found=true;matched=true}}}
  if !found{return "",nil,false,nil}
 }
 if !matched{return "",nil,false,nil}
 var identity struct{Scope string;EventID string;PayloadSHA256 string};_ = identity;return "",nil,false,fmt.Errorf("settled failure routes require the canonical invocation owner; the execution-journal owner does not implement them")
 raw,err:=json.Marshal(state);if err!=nil{return "",nil,false,err};sum:=sha256.Sum256(raw)
 capsule:=map[string]any{"schema":"praxis.process_settled_failures.v1","scope":identity.Scope,"event_id":identity.EventID,"payload_sha256":identity.PayloadSHA256,"phase":phase,"round":round,"incoming_state_sha256":hex.EncodeToString(sum[:]),"activations":[]any{}}
 activations:=[]any{}
 for _,record:=range records{
  activation,_:=record["activation"].(string);key,_:=record["node"].(string)
  cp,err:=r.claim.DurableNode(activation);if err!=nil{return "",nil,false,err};if cp==nil||cp.State!="settled"||cp.Outcome==nil{return "",nil,false,nil}
  if record["state"]=="failed"{if cp.Outcome.Error==""||len(cp.Outcome.Failure)==0{return "",nil,false,fmt.Errorf("handled failure lacks exact typed artifact")};var fields map[string]any;if err:=decodeProcessJSON(cp.Outcome.Failure,&fields);err!=nil{return "",nil,false,err};a,_:=json.Marshal(fields);b,_:=json.Marshal(record["failure"]);if !bytes.Equal(a,b)||cp.Outcome.Error!=record["error"]{return "",nil,false,fmt.Errorf("handled failure artifact differs")}}
  raw,err:=json.Marshal(cp);if err!=nil{return "",nil,false,err};activations=append(activations,map[string]any{"node":key,"activation":activation,"checkpoint_json":string(raw)})
 }
 capsule["activations"]=activations;return handler,capsule,true,nil
}

func resolveActivationContexts(nodes []processNode,level []string,r *recoveryRun,phase string,round int,f cliFlags)error{
 for _,node:=range nodes{for _,source:=range node.runtime{if source=="activation_context"{return fmt.Errorf("activation context requires canonical invocation-owner custody")}}};return nil
}

type runtimeSettledCheckpointBinding struct { Source string; Capsule struct { Node string; Field string } }
func settledCheckpointBindings(doc map[string]any)([]runtimeSettledCheckpointBinding,error){ if doc["settled_checkpoints"]!=nil{return nil,fmt.Errorf("settled checkpoint requires invocation-owner custody")};return nil,nil }
func resolveSettledCheckpoints(nodes []processNode,level []string,r *recoveryRun)error{for _,n:=range nodes{for _,source:=range n.runtime{if source=="settled_checkpoint"{return fmt.Errorf("settled checkpoint requires invocation-owner custody")}}};return nil}
func attachSettledCheckpoints(record map[string]any,r *recoveryRun,activation string){}

type runtimeFrontier struct{}

func parseWorkerFrontier(processDoc, doc map[string]any, nodes []processNode, edges []processEdge, specs map[string][]map[string]any, kinds map[string]string) (*progressionDeclaration, error) {
	return nil, fmt.Errorf("frontier requires the canonical invocation owner")
}
func runWorkerFrontier(d *progressionDeclaration, nodes []processNode, edges []processEdge, specs map[string][]map[string]any, bv boundView, r *recoveryRun, comp string, boundary *processRun, f cliFlags, env string, results map[string]map[string]any, failures, unknowns map[string]string, typed map[string]map[string]any, values map[string]string, c *continuationRun) ([]map[string]any, error) {
	return nil, fmt.Errorf("frontier requires the canonical invocation owner")
}

func validateRuntimePortBindings(nodes []processNode,edges []processEdge)error{
 for _,node:=range nodes{for field:=range node.runtime{
  if _,present:=node.fills[field];present{return fmt.Errorf("private runtime input %s.%s conflicts with supplied fill",node.key,field)}
  for _,edge:=range edges{if edge.toNode==node.key && edge.toField==field{return fmt.Errorf("private runtime input %s.%s conflicts with edge",node.key,field)}}
 }};return nil
}
// Stable authority inputs bind cached outcomes and recovery checkpoints. Every
// retry still authenticates afresh. A changing decision/call identifier does
// not force a duplicate effect; actor/profile/policy/scope/raw-input drift does.
func invocationAuthorityCompatibility(frame map[string]any)map[string]any{
 actor:=map[string]any{};for key,value:=range frame["actor"].(map[string]any){if key!="decision_ref"{actor[key]=value}}
 binding:=map[string]any{"actor":actor};for _,key:=range []string{"schema","process","package_digest","contract_digest","exposure","boundary_kind","boundary_name","event_id","input_digest"}{binding[key]=frame[key]};return binding
}
func invocationAuthorityDigest(value string,prefix bool) bool {
 if prefix {if !strings.HasPrefix(value,"sha256:"){return false};value=strings.TrimPrefix(value,"sha256:")}
 if len(value)!=64{return false};_,err:=hex.DecodeString(value);return err==nil
}
func invocationAuthorityObject(value any,keys ...string)(map[string]any,error){
 obj,ok:=value.(map[string]any);if !ok || len(obj)!=len(keys){return nil,fmt.Errorf("invalid invocation authority object")}
 for _,key:=range keys{if _,ok:=obj[key];!ok{return nil,fmt.Errorf("invocation authority field %s absent",key)}};return obj,nil
}
func invocationAuthorityString(obj map[string]any,key string,empty bool)(string,error){
 value,ok:=obj[key].(string);if !ok || strings.TrimSpace(value)!=value || (!empty && value==""){return "",fmt.Errorf("invalid invocation authority %s",key)};return value,nil
}
// Only the private inherited descriptor supplies this frame. The host's current
// authentication and permission decision is the authority; this transport never
// upgrades public input, attribution strings, or an operating-system username.
func readInvocationAuthority(nodes []processNode,boundary *processRun,f cliFlags)(map[string]any,error){
 required:=false;for _,node:=range nodes{for _,source:=range node.runtime{if source=="invocation_authority"{required=true}}}
 if !required {if f.invocationAuthorityFD!=0{return nil,fmt.Errorf("unexpected invocation authority transport")};return nil,nil}
 if boundary==nil || f.invocationAuthorityFD==0 || f.eventID=="" || boundary.invInputDigest=="" {return nil,fmt.Errorf("current native invocation authority required")}
 file:=os.NewFile(uintptr(f.invocationAuthorityFD),"invocation-authority");if file==nil{return nil,fmt.Errorf("invocation authority descriptor absent")};defer file.Close()
 info,err:=file.Stat();if err!=nil || info.Mode()&os.ModeNamedPipe==0{return nil,fmt.Errorf("invocation authority requires private pipe")}
 raw,err:=io.ReadAll(file);if err!=nil{return nil,err};var decoded map[string]any;if err=decodeProcessJSON(raw,&decoded);err!=nil{return nil,err}
 frame,err:=invocationAuthorityObject(decoded,"schema","actor","native_invocation","process","package_digest","contract_digest","exposure","boundary_kind","boundary_name","event_id","input_digest");if err!=nil{return nil,err}
 for _,key:=range []string{"schema","package_digest","exposure","boundary_kind","boundary_name","event_id","input_digest"}{if _,err=invocationAuthorityString(frame,key,false);err!=nil{return nil,err}}
 if _,err=invocationAuthorityString(frame,"contract_digest",true);err!=nil{return nil,err}
 if frame["schema"]!="praxis.invocation_authority.v1" || frame["package_digest"]!=f.invocationAuthorityPackageDigest || frame["event_id"]!=f.eventID || frame["input_digest"]!=boundary.invInputDigest || frame["boundary_kind"]!=boundary.exposure["role"] || frame["boundary_name"]!=boundary.exposure["name"] {return nil,fmt.Errorf("invocation authority execution binding mismatch")}
 if !invocationAuthorityDigest(f.invocationAuthorityPackageDigest,true){return nil,fmt.Errorf("invalid invocation authority package digest")}
 process,err:=invocationAuthorityObject(frame["process"],"name","digest");if err!=nil{return nil,err}
 for _,key:=range []string{"name","digest"}{if _,err=invocationAuthorityString(process,key,false);err!=nil{return nil,err};if process[key]!=boundary.ref[key]{return nil,fmt.Errorf("invocation authority process mismatch")}}
 actor,err:=invocationAuthorityObject(frame["actor"],"principal_ref","account","realm","profile_id","profile_revision","policy_digest","delegation_ref","decision_ref");if err!=nil{return nil,err}
 for _,key:=range []string{"principal_ref","account","realm","profile_id","policy_digest","decision_ref"}{if _,err=invocationAuthorityString(actor,key,false);err!=nil{return nil,err}}
 if _,err=invocationAuthorityString(actor,"delegation_ref",true);err!=nil{return nil,err}
 revision,ok:=actor["profile_revision"].(json.Number);if !ok{return nil,fmt.Errorf("invalid authenticated profile revision")};if value,e:=strconv.ParseUint(string(revision),10,64);e!=nil || value==0{return nil,fmt.Errorf("invalid authenticated profile revision")}
 policy,_:=actor["policy_digest"].(string);if !invocationAuthorityDigest(policy,false){return nil,fmt.Errorf("invalid authenticated policy digest")}
 native,err:=invocationAuthorityObject(frame["native_invocation"],"RunRef","CallID","InvocationID","MenuDigest");if err!=nil{return nil,err}
 for _,key:=range []string{"RunRef","CallID","InvocationID","MenuDigest"}{if _,err=invocationAuthorityString(native,key,false);err!=nil{return nil,err}}
 run,_:=native["RunRef"].(string);call,_:=native["CallID"].(string);digest:=sha256.Sum256([]byte(run+"\x00"+call))
 if native["InvocationID"]!="toolrun_"+hex.EncodeToString(digest[:]){return nil,fmt.Errorf("native invocation binding mismatch")}
 menu,_:=native["MenuDigest"].(string);if !invocationAuthorityDigest(menu,true){return nil,fmt.Errorf("invalid native menu binding")}
 boundary.invocationAuthorityBinding=invocationAuthorityCompatibility(frame)
 return frame,nil
}

func runExecutionControl(j *executionjournal.Journal,b *processRun,f cliFlags) error {
 if j==nil || b==nil{return fmt.Errorf("execution control requires the selected Process journal")}
 var controlErr error
 if f.executionControl=="cancel"{controlErr=j.Cancel(f.controlSequence,f.controlReason)}
 cancellation,err:=j.Cancellation();if err!=nil{return err}
 if controlErr!=nil && !(errors.Is(controlErr,executionjournal.ErrConflict) && cancellation!=nil){return controlErr}
 sequence,err:=j.ControlSequence();if err!=nil{return err}
 terminal,terminalErr:=j.Terminal()
 if terminalErr!=nil && !(errors.Is(terminalErr,executionjournal.ErrConflict) && cancellation!=nil){return terminalErr}
 if cancellation!=nil{terminal=executionjournal.TerminalCancelled}
 if terminal==""{terminal="running"}
 result:=map[string]any{"schema":"praxis.execution_control_result.v1","ok":true,"process":b.ref,"event_id":f.eventID,"event_payload_digest":j.PayloadDigest(),"control_sequence":sequence,"state":terminal,"completion_conflict":terminalErr!=nil}
 if cancellation!=nil{result["cancellation"]=cancellation}
 raw,err:=json.Marshal(result);if err!=nil{return err};fmt.Println(string(raw));return nil
}

func printDevelopmentOutcome(raw []byte) {
 var result map[string]any
 dec:=json.NewDecoder(bytes.NewReader(raw)); dec.UseNumber()
 if err:=dec.Decode(&result);err!=nil { fmt.Println(string(raw));return }
 result["development"]=true; result["admitted"]=false
 encoded,err:=json.Marshal(result);if err!=nil {panic(err)};fmt.Println(string(encoded))
}
