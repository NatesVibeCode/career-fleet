// career-fleet-author-closure-sha256: ea789dd77002f1d9087507b2747122b06afa6895088e3a524df86505c034e478
package extract

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := ExtractWork(a.State)
	return Output(&struct {
		State State `json:"state"`
	}{s})
}
