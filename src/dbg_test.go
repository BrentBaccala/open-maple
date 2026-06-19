package main
import ("os";"testing")
func TestDbgType(t *testing.T){
 if os.Getenv("OPENMAPLE_CAS")!="sage"{t.Skip()}
 it:=NewInterp()
 chk := []string{
  "type(1, extended_numeric);",        // true
  "type(infinity, extended_numeric);", // true
  "type(infinity, numeric);",          // false in Maple
  "type([1,2], list(extended_numeric));", // true
  "type([infinity,infinity], list(extended_numeric));", // true
  "type([1,2], list(integer));",       // true
  "type([1,x], list(extended_numeric));", // false
  "type(-infinity, extended_numeric);", // true
 }
 for _,c:=range chk {
  v,e:=it.Exec(c)
  t.Logf("%-45s => %s   e=%v",c,printValue(v),e)
 }
}
