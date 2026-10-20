// career-fleet-author-closure-sha256: a633b50ff316272f96f093a35ae2c8cadef01a2c07a2ceb51ca7224e929eb10f
package inspect

func Entry(in map[string]any)(map[string]any,error){var a struct{State State `json:"state"`};if e:=Decode(in,&a);e!=nil{return nil,e};s:=InspectWork(a.State);return Output(&struct{State State `json:"state"`;Next string `json:"next"`}{s,s.Phase})}
