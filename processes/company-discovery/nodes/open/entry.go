package open

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		Request Request `json:"request"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s, e := Open(a.Request)
	if e != nil {
		return nil, e
	}
	return Output(&struct {
		State State `json:"state"`
	}{s})
}
