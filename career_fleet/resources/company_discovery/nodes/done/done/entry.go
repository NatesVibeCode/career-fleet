package done

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := a.State
	r := Report(s)
	return Output(&struct {
		State       State  `json:"state"`
		Result      Result `json:"result"`
		Disposition string `json:"disposition"`
	}{s, r, "complete"})
}
