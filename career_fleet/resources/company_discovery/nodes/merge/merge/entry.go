// career-fleet-author-closure-sha256: e64143ca0aea7cd3392d7108310838afbf43a7547591b3f164a355e548117f60
package merge

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := MergeWork(a.State)
	return Output(&struct {
		State State `json:"state"`
	}{s})
}
