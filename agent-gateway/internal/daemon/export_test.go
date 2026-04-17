package daemon

// SetVerifyCommForTest replaces the package-level verifyComm hook and returns
// a function that restores the original. Intended for use in tests:
//
//	restore := daemon.SetVerifyCommForTest(myFake)
//	defer restore()
func SetVerifyCommForTest(fn func(pid int) (bool, error)) func() {
	old := verifyComm
	verifyComm = fn
	return func() { verifyComm = old }
}
