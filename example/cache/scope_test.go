package cache_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// RetrievedClient is a value scope: a run of assertions that all concern one
// value, grouped so the value is named once.
//
// It needs nothing from mokkit. A scope is just another vocabulary type — one
// that embeds the chain and carries the value alongside it. C# needed
// ThenValueScope because its chain was deferred and the value had to be
// threaded into steps that had not run yet; here the value is simply a field.
type RetrievedClient struct {
	*mokkit.Chain

	got *clients.Client
}

// Retrieved opens the scope. Returning to the outer chain is just using the
// embedded *Chain, or starting a fresh f.Inspect().
func (i Inspect) Retrieved(got *clients.Client) RetrievedClient {
	i.Helper()

	return RetrievedClient{Chain: i.Chain, got: got}
}

func (s RetrievedClient) Found() RetrievedClient {
	s.Helper()
	s.Add("Retrieved.Found", func(context.Context, mokkit.Host) error {
		if s.got == nil {
			return errors.New("want a client, got nothing")
		}

		return nil
	})

	return s
}

func (s RetrievedClient) Nothing() RetrievedClient {
	s.Helper()
	s.Add("Retrieved.Nothing", func(context.Context, mokkit.Host) error {
		if s.got != nil {
			return fmt.Errorf("want nothing, got %+v", *s.got)
		}

		return nil
	})

	return s
}

func (s RetrievedClient) Named(want string) RetrievedClient {
	s.Helper()
	s.Add("Retrieved.Named", func(context.Context, mokkit.Host) error {
		if s.got == nil {
			return fmt.Errorf("want a client named %q, got nothing", want)
		}
		if s.got.Name != want {
			return fmt.Errorf("want name %q, got %q", want, s.got.Name)
		}

		return nil
	})

	return s
}

func (s RetrievedClient) Active() RetrievedClient {
	s.Helper()
	s.Add("Retrieved.Active", func(context.Context, mokkit.Host) error {
		if s.got == nil {
			return errors.New("want an active client, got nothing")
		}
		if s.got.Status != clients.StatusActive {
			return fmt.Errorf("want status %v, got %v", clients.StatusActive, s.got.Status)
		}

		return nil
	})

	return s
}
