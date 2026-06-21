package main
import ("os"; "testing")
func TestJVTrace(t *testing.T) {
	if os.Getenv("OPENMAPLE_CAS") != "sage" { t.Skip() }
	it := NewInterp()
	it.LoadDifferentialThomas(dtSrcDir())
	it.Exec("`DifferentialThomas/ComputeRanking`([x,y,z],[u]);")
	it.Exec("`DifferentialThomas/DifferentialThomasDecomposition`([u[1,1,3]-u[4,0,0], u[5,1,0]-u[0,4,0], u[0,6,0], u[4,2,0]], []);")
}
