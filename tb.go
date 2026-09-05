package mokkit

// TB is the part of *testing.T that mokkit reports through. *testing.T and
// *testing.B satisfy it.
//
// Unlike testing.TB it can be implemented — by a fake reporter, or by a runner
// that is not the standard one — and it is wide enough for the common assertion
// libraries, so it can be handed straight to assert.Equal or require.NoError.
type TB interface {
	Helper()
	Name() string
	Cleanup(func())
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	FailNow()
	Failed() bool
}
