// career-fleet-author-closure-sha256: ae0b56369ac1e5d1d474390321089f90e89701affee6108579e621a8043493b1
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
