package main

import (
	node0 "discovery.local/process/nodes/bind/bind"
	node1 "discovery.local/process/nodes/boundrequest/boundrequest"
	node2 "discovery.local/process/nodes/browsercapture/browsercapture"
	node3 "discovery.local/process/nodes/browsercapturerecover/browsercapturerecover"
	node4 "discovery.local/process/nodes/capture/capture"
	node5 "discovery.local/process/nodes/capturerecover/capturerecover"
	node6 "discovery.local/process/nodes/captureload/captureload"
	node7 "discovery.local/process/nodes/captureretain/captureretain"
	node8 "discovery.local/process/nodes/conditions/conditions"
	node9 "discovery.local/process/nodes/done/done"
	node10 "discovery.local/process/nodes/extract/extract"
	node11 "discovery.local/process/nodes/identity/identity"
	node12 "discovery.local/process/nodes/inspect/inspect"
	node13 "discovery.local/process/nodes/loadrecover/loadrecover"
	node14 "discovery.local/process/nodes/match/match"
	node15 "discovery.local/process/nodes/merge/merge"
	node16 "discovery.local/process/nodes/open/open"
	node17 "discovery.local/process/nodes/parse/parse"
	node18 "discovery.local/process/nodes/planwork/planwork"
	node19 "discovery.local/process/nodes/request/request"
	node20 "discovery.local/process/nodes/retainrecover/retainrecover"
	node21 "discovery.local/process/nodes/selectwork/selectwork"
)

// dispatch selects exact Node revisions, never names alone.
var dispatch = map[nodeRef]nodeEntry{
	nodeRef{name:"discovery.bind.v1",digest:"9e8f57414138184dc994094dcd7309dd2cc488736f23b6d76d6acd6b167bed51"}: {run: node0.Entry, spec: "nodes/bind/bind/spec.json"},
	nodeRef{name:"discovery.bind_source_block.v1",digest:"fe54c24b23d7214ec6da8636d0a600473c2ab089f428db3d37ae97e8839b4d3e"}: {run: node1.Entry, spec: "nodes/boundrequest/boundrequest/spec.json"},
	nodeRef{name:"discovery.browser_capture.v1",digest:"b449a884df459350aaf163e0581f5de241c939846cfe4f7c82140e63f3005247"}: {scoped: node2.Entry, spec: "nodes/browsercapture/browsercapture/spec.json"},
	nodeRef{name:"discovery.browser_capture_recovery.v1",digest:"775023cfb1c13e28502dc584d3d7cda2280d78994512e4c8c5ed603bc7280bfe"}: {scoped: node3.Entry, spec: "nodes/browsercapturerecover/browsercapturerecover/spec.json"},
	nodeRef{name:"discovery.capture.v1",digest:"4888fd4e01af98ef88b06bf1e3e613f055739519c4f3ec5ebeade425ccc52c8e"}: {scoped: node4.Entry, spec: "nodes/capture/capture/spec.json"},
	nodeRef{name:"discovery.capture_recovery.v1",digest:"96c6b1dd09a268c406658dda4cc29967c25be4a2d55b7a6a65ab0c7b5f48c7a4"}: {scoped: node5.Entry, spec: "nodes/capturerecover/capturerecover/spec.json"},
	nodeRef{name:"discovery.captureload.v1",digest:"bd5d4fdff34c02168bd6c578940aca6ce7620b267df60a8ef8ebf815dcb4b128"}: {scoped: node6.Entry, spec: "nodes/captureload/captureload/spec.json"},
	nodeRef{name:"discovery.captureretain.v1",digest:"f703a290b4493744da41d261ee237f9cb706fc8a80f33c15815d43b35d117c56"}: {scoped: node7.Entry, spec: "nodes/captureretain/captureretain/spec.json"},
	nodeRef{name:"discovery.conditions.v1",digest:"cd106d527a8595b22607d8fb0f7891cd8e9485632d02ee13d54c4bd2a42ce384"}: {run: node8.Entry, spec: "nodes/conditions/conditions/spec.json"},
	nodeRef{name:"discovery.done.v1",digest:"0fabdac1c7caf6f4d560a77fbbf4f0976352608b05ddf2c21ded2b9db70f9125"}: {run: node9.Entry, spec: "nodes/done/done/spec.json"},
	nodeRef{name:"discovery.extract.v1",digest:"7e0b83cde3d2acd6db8ff03520374cabebd14a31cd994dfc91566843c869425e"}: {run: node10.Entry, spec: "nodes/extract/extract/spec.json"},
	nodeRef{name:"discovery.identity.v1",digest:"08b731771da607413d57c8689d6642cf7be05b8265f7ead503996eb9d2f1dcce"}: {run: node11.Entry, spec: "nodes/identity/identity/spec.json"},
	nodeRef{name:"discovery.inspect.v1",digest:"e2ce31b778911f5a14ee50342c8844d9db57a3d8c4611d893a6f956e9b2b38b7"}: {run: node12.Entry, spec: "nodes/inspect/inspect/spec.json"},
	nodeRef{name:"discovery.loadrecover.v1",digest:"d34f717adc344ac8cd277c74c509db27eabd136d6f2aa0cf7bd1bbc0afbe5d44"}: {scoped: node13.Entry, spec: "nodes/loadrecover/loadrecover/spec.json"},
	nodeRef{name:"discovery.match.v1",digest:"31b5ca36661b6f5ab24c1ad9d5088d244f7fdd7e15f0a1c0a5236a531cee5ebb"}: {run: node14.Entry, spec: "nodes/match/match/spec.json"},
	nodeRef{name:"discovery.merge.v1",digest:"288c21de0489b5b80db8a2e093063d254697bf7c70dd5ce2d28794d13f89b9a1"}: {run: node15.Entry, spec: "nodes/merge/merge/spec.json"},
	nodeRef{name:"discovery.open.v1",digest:"30eb93c84cc71d342bdbb56660c2adf52d5f0dccb1c1eadb70b739a3cdb5be49"}: {run: node16.Entry, spec: "nodes/open/open/spec.json"},
	nodeRef{name:"discovery.parse.v1",digest:"3be3572f608cca5f98fbc802280270cf7586c5a52774f01e7084b51e6c5ccd9e"}: {run: node17.Entry, spec: "nodes/parse/parse/spec.json"},
	nodeRef{name:"discovery.planwork.v1",digest:"65e781b50b5da8cdf4fe59c98577da931b8c6c093176f8dce8470d1a01375c36"}: {run: node18.Entry, spec: "nodes/planwork/planwork/spec.json"},
	nodeRef{name:"discovery.request.v1",digest:"3d0ba0684ac3abf40762030d1ef78959936c775b132d913cadd0221064c52fad"}: {run: node19.Entry, spec: "nodes/request/request/spec.json"},
	nodeRef{name:"discovery.retainrecover.v1",digest:"1a03cf216f00e5839f7d26fa3ff882be25cf4123cf4679d7dbed9860e78f2d2c"}: {scoped: node20.Entry, spec: "nodes/retainrecover/retainrecover/spec.json"},
	nodeRef{name:"discovery.selectwork.v1",digest:"75ea33a7442cb70ef5fe13b1f5664245ccf2b69e745631077f20a02b0623b02d"}: {run: node21.Entry, spec: "nodes/selectwork/selectwork/spec.json"},
}
