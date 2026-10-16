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
