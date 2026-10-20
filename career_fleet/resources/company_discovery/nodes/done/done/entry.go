// career-fleet-author-closure-sha256: 23d2f2c881c7f783ff53246fc82451bfc628a49a2b4ee2344b840986ddad7357
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
