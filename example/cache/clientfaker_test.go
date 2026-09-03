package cache_test

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// FixedNow keeps generated clients deterministic, so a serialized client is
// byte-for-byte comparable between what a verb arranges and what the subject
// stores.
var FixedNow = time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)

// clientFaker builds plausible clients from a fixed seed. It stands in for the
// original's Bogus faker, without the dependency.
type clientFaker struct{ rnd *rand.Rand }

func newClientFaker() *clientFaker { return &clientFaker{rnd: rand.New(rand.NewSource(20260115))} }

var (
	companies = []string{"Acme", "Globex", "Initech", "Umbrella", "Soylent", "Hooli"}
	suffixes  = []string{"Corporation", "Industries", "Holdings", "Systems", "Labs"}
)

// ClientOpt is the mutator shape the vocabulary composes: defaults at the top,
// variation passed in by the test.
type ClientOpt func(*clients.Client)

func WithName(name string) ClientOpt        { return func(c *clients.Client) { c.Name = name } }
func WithEmail(email string) ClientOpt      { return func(c *clients.Client) { c.Email = email } }
func WithStatus(s clients.Status) ClientOpt { return func(c *clients.Client) { c.Status = s } }
func WithID(id string) ClientOpt            { return func(c *clients.Client) { c.ID = id } }

func (f *clientFaker) newClient(opts ...ClientOpt) clients.Client {
	n := f.rnd.Intn(len(companies))
	name := fmt.Sprintf("%s %s", companies[n], suffixes[f.rnd.Intn(len(suffixes))])

	client := clients.Client{
		ID:        fmt.Sprintf("client-%04d", f.rnd.Intn(10000)),
		Name:      name,
		Email:     fmt.Sprintf("contact@%s.example", companies[n]),
		Phone:     fmt.Sprintf("+1%010d", f.rnd.Intn(1_000_000_000)),
		Status:    clients.StatusActive,
		CreatedAt: FixedNow,
		UpdatedAt: FixedNow,
	}
	for _, opt := range opts {
		opt(&client)
	}

	return client
}
