// career-fleet-author-closure-sha256: 211c610d476cc9ea06e58d8c0573c09023a7d10f821533604c042dd48b30199f
package captureretain
import("context";"crypto/sha256";"encoding/hex";"encoding/json";"fmt";"discovery.local/process/internal/egresscontract")
func scope()string{h:=sha256.Sum256([]byte("career-fleet.capture-bodies.v1"));return hex.EncodeToString(h[:])}
func textDigest(s string)string{h:=sha256.Sum256([]byte(s));return hex.EncodeToString(h[:])}
func Entry(client egresscontract.Client,in map[string]any)(map[string]any,error){var a struct{Capture Capture `json:"capture"`};if e:=Decode(in,&a);e!=nil{return nil,e};c:=a.Capture
if c.BodyBase64!="" {content:=c.BodyBase64;digest:=textDigest(content);request,_:=json.Marshal(map[string]any{"scope_key":scope(),"key":c.SHA256,"content":content,"expected_digest":""});response,e:=client.Do(context.Background(),egresscontract.OperationRequest{Operation:"retain",Body:request});if e!=nil{return nil,e};var result struct{Record struct{Digest string `json:"digest"`;Found bool `json:"found"`} `json:"record"`};if e=json.Unmarshal(response.Body,&result);e!=nil||!result.Record.Found||result.Record.Digest!=digest{return nil,fmt.Errorf("capture body publication not confirmed")};c.BodyRecord=c.SHA256;c.BodyRecordDigest=digest;c.BodyBase64=""}
return Output(&struct{Capture Capture `json:"capture"`}{c})}
