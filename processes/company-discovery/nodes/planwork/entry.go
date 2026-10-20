// career-fleet-author-closure-sha256: 5efd5e50f68aabd5b789a7435488f97c31dd9ccd13c34ef91f2ed6def7381e28
package planwork

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := PlanQuestions(a.State)
	return Output(&struct {
		State State  `json:"state"`
		Next  string `json:"next"`
	}{s, "select"})
}
