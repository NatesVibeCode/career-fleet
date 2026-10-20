// career-fleet-author-closure-sha256: 4f3d897be670ae8b00c638cef0cab659bfae833f3981d85e10931af29bd628fd
package selectwork

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s, next := SelectWork(a.State)
	return Output(&struct {
		State State  `json:"state"`
		Next  string `json:"next"`
	}{s, next})
}
