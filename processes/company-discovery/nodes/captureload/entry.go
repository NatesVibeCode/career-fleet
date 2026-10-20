// career-fleet-author-closure-sha256: 31638532380d9fbb3088af88da07b772dec926920b89f4c185a00a3f0695e2f2
package captureload
import("context";"crypto/sha256";"encoding/hex";"encoding/json";"fmt";"github.com/NatesVibeCode/forge/egresscontract")
func scope()string{h:=sha256.Sum256([]byte("career-fleet.capture-bodies.v1"));return hex.EncodeToString(h[:])}
func textDigest(s string)string{h:=sha256.Sum256([]byte(s));return hex.EncodeToString(h[:])}
func Entry(client egresscontract.Client,in map[string]any)(map[string]any,error){var a struct{State State `json:"state"`};if e:=Decode(in,&a);e!=nil{return nil,e};s:=a.State;c:=&s.Capture
if c.BodyRecord!=""&&c.BodyBase64=="" {if c.BodyRecord!=c.SHA256{return nil,fmt.Errorf("capture record identity mismatch")};request,_:=json.Marshal(map[string]any{"scope_key":scope(),"key":c.BodyRecord,"content":"","expected_digest":""});response,e:=client.Do(context.Background(),egresscontract.OperationRequest{Operation:"load",Body:request});if e!=nil{return nil,e};var result struct{Record struct{Content string `json:"content"`;Digest string `json:"digest"`;Found bool `json:"found"`} `json:"record"`};if e=json.Unmarshal(response.Body,&result);e!=nil||!result.Record.Found||result.Record.Digest!=c.BodyRecordDigest||textDigest(result.Record.Content)!=c.BodyRecordDigest{return nil,fmt.Errorf("capture body record unavailable or changed")};c.BodyBase64=result.Record.Content}
return Output(&struct{State State `json:"state"`}{s})}
