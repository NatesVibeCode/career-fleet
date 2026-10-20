package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NatesVibeCode/forge/forge"
)

func main() {
	if len(os.Args) != 2 {
		panic("usage: refresh-recovery PLAN")
	}
	planPath := os.Args[1]
	planBytes, err := os.ReadFile(planPath)
	if err != nil {
		panic(err)
	}
	var plan map[string]any
	if err := json.Unmarshal(planBytes, &plan); err != nil {
		panic(err)
	}
	process := plan["process"].(map[string]any)
	policy := process["policy"].(map[string]any)
	recovery := policy["recovery"].(map[string]any)
	bindings := recovery["bindings"].([]any)
	root := filepath.Dir(planPath)
	for _, rawBinding := range bindings {
		binding := rawBinding.(map[string]any)
		target := binding["target"].(map[string]any)
		specName := "capturerecover"
		if target["block"] == "acquire_browser" && target["node"]=="browsercapture" {
			specName = "browsercapturerecover"
		}
		if target["node"]=="captureretain"{specName="retainrecover"}
        if target["node"]=="captureload"{specName="loadrecover"}
		specBytes, err := os.ReadFile(filepath.Join(root, "specs", specName+".json"))
		if err != nil {
			panic(err)
		}
		spec, refusal := forge.Parse(specBytes)
		if refusal != nil {
			panic(refusal)
		}
		node, refusal := forge.NodeFromSpec(spec)
		if refusal != nil {
			panic(refusal)
		}
		reconciler := binding["reconciler"].(map[string]any)
		blockValue := reconciler["block"].(map[string]any)
		encoded, err := json.Marshal(blockValue)
		if err != nil {
			panic(err)
		}
		var block forge.Block
		if err := json.Unmarshal(encoded, &block); err != nil {
			panic(err)
		}
		if len(block.Nodes) != 1 {
			panic("recovery Block must have one reconciler Node")
		}
		block.Nodes[0].Node = node
		ref, refusal := block.Reference()
		if refusal != nil {
			panic(refusal)
		}
		reconciler["block"] = block
		reconciler["ref"] = map[string]any{"name": ref.Name, "digest": ref.Digest}
		fmt.Printf("%s %s@%s\n", specName, ref.Name, ref.Digest)
	}
	updated, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(planPath, append(updated, '\n'), 0o644); err != nil {
		panic(err)
	}
}
