// career-fleet-author-closure-sha256: 11a8f956788545ab02e1473fe1de94a179bb4e111316305c183960d66daedddf
package conditions

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := ConditionsWork(a.State)
	return Output(&struct {
		State State `json:"state"`
	}{s})
}
