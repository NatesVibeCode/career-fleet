// career-fleet-author-closure-sha256: 4f29bdc3dddaca0fe155ad9a444ff5078eddbef7bcc01cfe9b67a00b23f3934c
package retainrecover
import("context";"crypto/sha256";"encoding/hex";"encoding/json";"fmt";"github.com/NatesVibeCode/forge/egresscontract")
func scope()string{h:=sha256.Sum256([]byte("career-fleet.capture-bodies.v1"));return hex.EncodeToString(h[:])}
func textDigest(s string)string{h:=sha256.Sum256([]byte(s));return hex.EncodeToString(h[:])}
func restore(client egresscontract.Client,in map[string]any)(map[string]any,error){var a struct{Capture Capture `json:"capture"`};if e:=Decode(in,&a);e!=nil{return nil,e};c:=a.Capture
if c.BodyBase64!="" {request,_:=json.Marshal(map[string]any{"scope_key":scope(),"key":c.SHA256,"content":"","expected_digest":""});response,e:=client.Do(context.Background(),egresscontract.OperationRequest{Operation:"load",Body:request});if e!=nil{return nil,e};var result struct{Record struct{Content string `json:"content"`;Digest string `json:"digest"`;Found bool `json:"found"`} `json:"record"`};if e=json.Unmarshal(response.Body,&result);e!=nil{return nil,e};if result.Record.Found {if result.Record.Digest!=textDigest(c.BodyBase64){return nil,fmt.Errorf("retained capture differs from original")};c.BodyRecord=c.SHA256;c.BodyRecordDigest=result.Record.Digest;c.BodyBase64=""}else{c.ErrorCode="inline_retention_recovery"}}
return Output(&struct{Capture Capture `json:"capture"`}{c})}

func Entry(client egresscontract.Client,in map[string]any)(map[string]any,error){digest,ok:=in["request_digest"].(string);if !ok||len(digest)!=64{return nil,fmt.Errorf("exact recovery request digest required")};input:=map[string]any{};for k,v:=range in{if k!="request_digest"{input[k]=v}};out,e:=restore(client,input);if e!=nil{return nil,e};out["request_digest"]=digest;out["status"]="completed";return out,nil}
