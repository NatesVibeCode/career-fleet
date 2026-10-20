// career-fleet-author-closure-sha256: 88e75694b88b37fe99b2d82f658c3bbbc0726d1b5ced7b3a720260cdaca74303
package identity

func Entry(in map[string]any)(map[string]any,error){var a struct{State State `json:"state"`};if e:=Decode(in,&a);e!=nil{return nil,e};s:=IdentityWork(a.State);return Output(&struct{State State `json:"state"`}{s})}
