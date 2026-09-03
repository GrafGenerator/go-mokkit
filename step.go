package mokkit

import "context"

// A StepFunc is the work one unit of a test does: set up a collaborator, run
// the operation under test, observe an outcome. It receives the stage's Host
// and reports failure by returning an error.
type StepFunc func(ctx context.Context, h Host) error

// A Step is a StepFunc with the name it reports under when it fails. It is the
// contract for vocabulary authored as plain functions — including in packages
// that cannot add methods to a chain's type — which Chain.And then runs:
//
//	func HasClient(c *Client) mokkit.Step {
//	    return mokkit.NewStep("cache.HasClient", func(ctx context.Context, h mokkit.Host) error {
//	        ...
//	    })
//	}
//
// The name is given rather than recovered from the runtime because the compiler
// may inline the verb that built the closure, which would attribute the step to
// whatever function called it.
type Step struct {
	Name string
	Run  StepFunc
}

// NewStep pairs a step's work with the name it reports under.
func NewStep(name string, fn StepFunc) Step {
	return Step{Name: name, Run: fn}
}
