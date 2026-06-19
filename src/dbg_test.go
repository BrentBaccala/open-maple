package main
import ("os";"testing")
func TestDbgQ(t *testing.T){
 if os.Getenv("OPENMAPLE_CAS")!="sage"{t.Skip()}
 it:=NewInterp()
 if e:=it.LoadDifferentialThomas(dtSrcDir());e!=nil{t.Fatal(e)}
 it.Exec("`DifferentialThomas/ComputeRanking`([x,y],[u]);")
 // build the system and inspect Q
 it.Exec("sys := `DifferentialThomas/ProcInput`([u[1,0]-u[0,0], u[0,1]-u[0,0]^2], [])[1]:")
 v,e:=it.Exec("sys[1]['Q'];")
 t.Logf("Q = %s e=%v",printValue(v),e)
 v2,_:=it.Exec("nops(sys[1]['Q']);")
 t.Logf("nops(Q)=%s",printValue(v2))
}
