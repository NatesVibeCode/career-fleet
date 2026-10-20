// career-fleet-author-closure-sha256: f581ea9f9d082cdcbd6b33caa899f8b94326dffd311a8d0b7bc382aed1563a54
package open

func Entry(in map[string]any) (map[string]any, error) {
	var a struct {
		Request Request `json:"request"`
	}
	if e := Decode(in, &a); e != nil {
		return nil, e
	}
	inventory,e := pinnedSourceBlocks()
 if e != nil {return nil,e}
 request, e := ResolveSourceBlocks(a.Request, inventory)
	if e != nil {
		return nil, e
	}
	s, e := Open(request)
	if e != nil {
		return nil, e
	}
	return Output(&struct {
		State State `json:"state"`
	}{s})
}
