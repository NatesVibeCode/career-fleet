package parse

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s, e := ParseWork(a.State)
	if e != nil {
		return nil, e
	}
	return Output(&struct {
		State State  `json:"state"`
		Next  string `json:"next"`
	}{s, s.Phase})
}
