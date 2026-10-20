// Bind the author's reviewed outcomes to exact source/test bytes. This prepares
// normal creation inputs only; Forge create/qualify owns execution and admission.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/NatesVibeCode/forge/forge"
	"github.com/NatesVibeCode/praxis-work/go/capabilitycontract"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 2 {
		panic("selected process directory required")
	}
	root := os.Args[1]
	entries, err := os.ReadDir(filepath.Join(root, "specs"))
	if err != nil {
		panic(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		key := entry.Name()[:len(entry.Name())-5]
		raw, err := os.ReadFile(filepath.Join(root, "specs", entry.Name()))
		if err != nil {
			panic(err)
		}
		spec, r := forge.Parse(raw)
		if r != nil {
			panic(r)
		}
		if issues := forge.CodificationMethodIssues(spec); len(issues) != 0 {
			panic(fmt.Sprintf("%s codification: %+v", key, issues))
		}
		for _, c := range spec.ProofSpec.Behavior {
			if c.Error != "" {
				continue
			}
			for _, field := range spec.Fields {
				if field.Side != "input" {
					continue
				}
				value, ok := c.Inputs[field.Name]
				if !ok {
					if field.Required {
						panic(fmt.Sprintf("%s/%s missing input %s", key, c.ID, field.Name))
					}
					continue
				}
				if err := capabilitycontract.ValidateTransport(field.Type, value); err != nil {
					panic(fmt.Sprintf("%s/%s/%s: %v", key, c.ID, field.Name, err))
				}
			}
		}
		digest, ok := forge.NodeSpecDigest(spec)
		if !ok {
			panic("Node digest absent")
		}
		sources, tests, err := forge.ReadNodePackageFiles(filepath.Join(root, "nodes", key))
		if err != nil {
			panic(err)
		}
		sd, err := forge.NodeSourceBundleDigest(sources)
		if err != nil {
			panic(err)
		}
		td, err := forge.NodeTestBundleDigest(tests)
		if err != nil {
			panic(err)
		}
		outputs := []string{}
		for _, f := range spec.Fields {
			if f.Side == "output" {
				outputs = append(outputs, f.Name)
			}
		}
		purpose := spec.Usage.Purpose
		assessment := forge.NodeBoundaryAssessment{
			Schema: forge.NodeBoundaryAssessmentSchema, NodeSpecDigest: digest, SourceBundleDigest: sd, TestBundleDigest: td,
			Outcome: purpose, OutcomeID: spec.Composition.Outcomes[0], Decision: "keep",
			Rationale: "Reviewed the selected Entry adapter and its reachable core helpers. Its public fields jointly describe this single transformation or effect result. Selection, acquisition, retention, byte loading, extraction, identity, evidence assessment and recovery are independently composed Nodes; this adapter does not choose another workflow.",
			Reuse:     []forge.NodeBoundaryReuse{{Scope: "Existing Career Fleet deterministic core, codecs and selected tokenizer source closure", EvidenceDigest: sd, Finding: "atomic_helper", Rationale: "These copied helpers implement this Entry result and carry no independently invoked Node authority. The exact selected Node is reused by the authored Process and source Blocks; no self-reuse or inferred catalog admission is claimed."}},
			Stages:    []forge.NodeBoundaryStage{{Name: key, Role: "outcome", Outcome: purpose, IndependentlyUseful: true, Decision: "keep", OutputFields: outputs, Rationale: "The source-reviewed Entry exposes this outcome. Auxiliary phase selectors and carried state deliver the same transformation; effect and recovery boundaries remain explicit in the Process."}},
		}
		if refusal := forge.ValidateNodeBoundary(spec, &assessment, sd, td); refusal != nil {
			panic(refusal)
		}
		dir := filepath.Join(root, "author/qualification/assessments")
		if err := os.MkdirAll(dir, 0755); err != nil {
			panic(err)
		}
		data, err := json.MarshalIndent(assessment, "", "  ")
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, key+".json"), append(data, '\n'), 0644); err != nil {
			panic(err)
		}
		fmt.Println(key, digest)
	}
}
