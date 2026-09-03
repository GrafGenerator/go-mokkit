// Package clients is the domain of the worked example: a client entity and the
// cache port the application depends on.
//
// It is a port of example/Example1 from the C# original, kept close enough to
// compare side by side. IDs are plain strings rather than UUIDs so the example
// adds no dependency of its own.
package clients

//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source clients.go -destination mocks.go -package clients

import (
	"context"
	"time"
)

type Status int

const (
	StatusActive    Status = 1
	StatusInactive  Status = 2
	StatusSuspended Status = 3
)

type Client struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DistributedCache is the infrastructure the application caches through. It is
// the collaborator the unit suite replaces with a double.
type DistributedCache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	Remove(ctx context.Context, key string) error
}
