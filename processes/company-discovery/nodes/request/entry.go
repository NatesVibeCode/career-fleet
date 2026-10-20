// career-fleet-author-closure-sha256: 5cd34abb0efbadcfa69b2cbb51227ce302f7cadaf80fa13b90067f3114dc8f1c
package request

import "fmt"

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		State State `json:"state"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	s := a.State
	if s.CurrentAction < 0 || s.CurrentAction >= len(s.Actions) {
		return nil, fmt.Errorf("discovery: selected action missing")
	}
	return Output(&struct {
		State State  `json:"state"`
		URL   string `json:"url"`
	}{s, s.Actions[s.CurrentAction].URL})
}
