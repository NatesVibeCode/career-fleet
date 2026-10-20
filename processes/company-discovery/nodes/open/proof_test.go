package open
import("testing";"encoding/json")
func TestProof(t *testing.T){var in map[string]any;if e:=json.Unmarshal([]byte(`{"request":"invalid"}`),&in);e!=nil{t.Fatal(e)};if _,e:=Entry(in);e==nil{t.Fatal("malformed input produced a result")}}
