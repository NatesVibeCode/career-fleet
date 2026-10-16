package bind

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State   State   `json:"state"`
		Capture Capture `json:"capture"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s, e := BindCapture(a.State, a.Capture)
	if e != nil {
		return nil, e
	}
	return Output(&struct {
		State State  `json:"state"`
		Next  string `json:"next"`
	}{s, "parse"})
}
