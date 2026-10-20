// career-fleet-author-closure-sha256: e37b79c845e3051745d4e6550c0178c30c204453f038ee8bf47586c765759be7
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
