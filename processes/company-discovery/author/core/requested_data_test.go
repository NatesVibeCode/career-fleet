package discovery

import "testing"

func TestRequestedDataHasNoRungOrRequiredFieldOrder(t *testing.T) {
	observation := Observation{ID: "observed", SourceID: "directory", URL: "https://example.com/company", EntityID: "company:one", EntityName: "One Company", Field: "source_result"}
	candidate := Candidate{ID: "entity:company:one", Name: "One Company", IdentityState: "unresolved", Observations: []Observation{observation}}
	questions := []Question{
		{ID: "custom_revenue", Field: "operator_revenue_measure", Scope: "2026", Acquisition: "source_document"},
		{ID: "custom_materials", Field: "operator_material_specification", Scope: "product", Acquisition: "source_document"},
	}
	for _, order := range [][]Question{questions, {questions[1], questions[0]}} {
		state := PlanQuestions(State{Request: Request{Questions: order}, Candidates: []Candidate{candidate}})
		if len(state.Actions) != 2 {
			t.Fatalf("arbitrary data request was gated by identity, field or order: %+v", state.Actions)
		}
		seen := map[string]bool{}
		for _, a := range state.Actions {
			seen[a.QuestionID] = true
			if a.URL != observation.URL || a.CandidateID != candidate.ID {
				t.Fatal("request lost selected business", a)
			}
		}
		if !seen[questions[0].ID] || !seen[questions[1].ID] {
			t.Fatal("question assigned to a fixed rung", seen)
		}
	}
}

func TestPriorBusinessCanRequestAFieldAgainInANewTask(t *testing.T) {
	observation := Observation{ID: "observed", SourceID: "directory", URL: "https://example.com/company", EntityID: "company:one", EntityName: "One Company", Field: "source_result"}
	prior := Candidate{ID: "entity:company:one", Observations: []Observation{observation}, Assertions: []Assertion{{Field: "custom_measure", Scope: "current", Value: "old value"}}}
	state, err := Open(Request{Objective: "Refresh any requested data for this listed business", Prior: []Candidate{prior}, Questions: []Question{{ID: "refresh_measure", Field: "custom_measure", Scope: "current", Acquisition: "source_document"}}})
	if err != nil {
		t.Fatal(err)
	}
	state = PlanQuestions(state)
	if len(state.Actions) != 1 || state.Actions[0].QuestionID != "refresh_measure" {
		t.Fatalf("prior data prevented an explicit new request: %+v", state.Actions)
	}
}
