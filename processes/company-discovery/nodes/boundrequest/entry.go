// career-fleet-author-closure-sha256: 1213076631a3a13f6933948b5cb8b20e9c0c067a467b699be0237c4943a6b298
package boundrequest
import "fmt"
func Entry(in map[string]any)(map[string]any,error){var a struct{State State `json:"state"`;Source Source `json:"source"`};if e:=Decode(in,&a);e!=nil{return nil,e};s,e:=BindSourceBlock(a.State,a.Source);if e!=nil{return nil,e};if s.CurrentAction<0||s.CurrentAction>=len(s.Actions){return nil,fmt.Errorf("selected action absent")};return Output(&struct{State State `json:"state"`;URL string `json:"url"`}{s,s.Actions[s.CurrentAction].URL})}
