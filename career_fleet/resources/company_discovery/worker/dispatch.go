package main

import (
	node0 "discovery.local/process/nodes/bind/bind"
	node1 "discovery.local/process/nodes/capture/capture"
	node2 "discovery.local/process/nodes/capturerecover/capturerecover"
	node3 "discovery.local/process/nodes/conditions/conditions"
	node4 "discovery.local/process/nodes/done/done"
	node5 "discovery.local/process/nodes/extract/extract"
	node6 "discovery.local/process/nodes/match/match"
	node7 "discovery.local/process/nodes/merge/merge"
	node8 "discovery.local/process/nodes/open/open"
	node9 "discovery.local/process/nodes/parse/parse"
	node10 "discovery.local/process/nodes/planwork/planwork"
	node11 "discovery.local/process/nodes/request/request"
	node12 "discovery.local/process/nodes/selectwork/selectwork"
)

// dispatch selects exact Node revisions, never names alone.
var dispatch = map[nodeRef]nodeEntry{
	nodeRef{name:"discovery.bind.v1",digest:"b13b857c380ae34e9e8b408cea9ca43626a1c13c8de709a64bd1b928ccf86e44"}: {run: node0.Entry, spec: "nodes/bind/bind/spec.json"},
	nodeRef{name:"discovery.capture.v1",digest:"68ba423392fdee4794838bd1a08163178849b663aa0dd5a18837b92ddca80bf1"}: {scoped: node1.Entry, spec: "nodes/capture/capture/spec.json"},
	nodeRef{name:"discovery.capture_recovery.v1",digest:"6a6301569ded543c150633b65283af6dd34da5afab4f2da8267752ca65ceaf4c"}: {scoped: node2.Entry, spec: "nodes/capturerecover/capturerecover/spec.json"},
	nodeRef{name:"discovery.conditions.v1",digest:"16d2953178f9458261f3dc5a15ee6c170649cae1dc73eff23f93674a466a6341"}: {run: node3.Entry, spec: "nodes/conditions/conditions/spec.json"},
	nodeRef{name:"discovery.done.v1",digest:"6381b75a9922ec09cdfec929b9592edf1bce98fb075fc4e0d51e3137caaf9db3"}: {run: node4.Entry, spec: "nodes/done/done/spec.json"},
	nodeRef{name:"discovery.extract.v1",digest:"55d92ea38a555c43dee7c8e2e2fe108c5cb6878f17945520a202592f2a6b06b8"}: {run: node5.Entry, spec: "nodes/extract/extract/spec.json"},
	nodeRef{name:"discovery.match.v1",digest:"49a4be8f294615b5c70620e4cd11217ea335e956588f5f497a3efbd74c7b0927"}: {run: node6.Entry, spec: "nodes/match/match/spec.json"},
	nodeRef{name:"discovery.merge.v1",digest:"84f1533f20ec6ca33573e3174f0f4c37947ae764108c23bfbe00097e61ce6a84"}: {run: node7.Entry, spec: "nodes/merge/merge/spec.json"},
	nodeRef{name:"discovery.open.v1",digest:"9b7896672f6517a4d25fc0475977dd35f1cff25d081ab1e2828c8c5d8bb8cf98"}: {run: node8.Entry, spec: "nodes/open/open/spec.json"},
	nodeRef{name:"discovery.parse.v1",digest:"df59d1b6d4fcc267d1d3be303cee3f5267c018930292b66f0dcd50d097b7dcaf"}: {run: node9.Entry, spec: "nodes/parse/parse/spec.json"},
	nodeRef{name:"discovery.planwork.v1",digest:"05ae35d7a644968ef677ef9fca31168799ff960b493ece6e32194e58b9549221"}: {run: node10.Entry, spec: "nodes/planwork/planwork/spec.json"},
	nodeRef{name:"discovery.request.v1",digest:"b87ff08a42eb4628c5b09e99f4172b7f0b477e04d686c327754e970b6b2d849e"}: {run: node11.Entry, spec: "nodes/request/request/spec.json"},
	nodeRef{name:"discovery.selectwork.v1",digest:"6db0d5535374f43240796d2e364ed564931b2f2c23de46572b06bfb062a0294f"}: {run: node12.Entry, spec: "nodes/selectwork/selectwork/spec.json"},
}
