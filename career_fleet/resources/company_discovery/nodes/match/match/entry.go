// career-fleet-author-closure-sha256: 396ad5da15c7596a2618a62426ecfbc0b3c3adcf541821985e9c6d2d93a3d4d3
package match

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := MatchWork(a.State)
	return Output(&struct {
		State State `json:"state"`
	}{s})
}
