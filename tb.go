package mokkit

// TB is the part of *testing.T that mokkit reports through. *testing.T and
// *testing.B satisfy it.
//
// It is deliberately narrower than testing.TB, which is sealed and so cannot be
// implemented — by mokkit's own tests, by a fake reporter, or by a runner that
// is not the standard one. It is also wide enough for the common assertion
// libraries, so a vocabulary type can be handed straight to assert.Equal or
// require.NoError.
type TB interface {
	Helper()
	Name() string
	Cleanup(func())
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	FailNow()
	Failed() bool
}
