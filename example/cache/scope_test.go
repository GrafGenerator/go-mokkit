package cache_test

import (
	"errors"
	"fmt"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// RetrievedClient is a value scope: a run of assertions that all concern one
// value, which is named once. A scope is a vocabulary type that embeds the
// chain and carries the value alongside it.
type RetrievedClient struct {
	*mokkit.Chain

	got *clients.Client
}

// Retrieved opens the scope. The outer chain is the embedded *Chain, or a
// fresh f.Inspect().
func (i Inspect) Retrieved(got *clients.Client) RetrievedClient {
	i.Helper()

	return RetrievedClient{Chain: i.Chain, got: got}
}

func (s RetrievedClient) Found() RetrievedClient {
	s.Helper()

	return mokkit.Do(s, func(mokkit.Host) error {
		if s.got == nil {
			return errors.New("want a client, got nothing")
		}

		return nil
	})
}

func (s RetrievedClient) Nothing() RetrievedClient {
	s.Helper()

	return mokkit.Do(s, func(mokkit.Host) error {
		if s.got != nil {
			return fmt.Errorf("want nothing, got %+v", *s.got)
		}

		return nil
	})
}

func (s RetrievedClient) Named(want string) RetrievedClient {
	s.Helper()

	return mokkit.Do(s, func(mokkit.Host) error {
		if s.got == nil {
			return fmt.Errorf("want a client named %q, got nothing", want)
		}
		if s.got.Name != want {
			return fmt.Errorf("want name %q, got %q", want, s.got.Name)
		}

		return nil
	})
}

func (s RetrievedClient) Active() RetrievedClient {
	s.Helper()

	return mokkit.Do(s, func(mokkit.Host) error {
		if s.got == nil {
			return errors.New("want an active client, got nothing")
		}
		if s.got.Status != clients.StatusActive {
			return fmt.Errorf("want status %v, got %v", clients.StatusActive, s.got.Status)
		}

		return nil
	})
}
