// Package shop is a small domain for exercising the gomock adapter: two
// collaborators behind interfaces, and a service wired over them.
package shop

//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -source shop.go -destination mocks.go -package shop

import "context"

type User struct {
	ID     string
	Status string
}

type UserRepository interface {
	ByID(ctx context.Context, id string) (User, error)
}

type RateRepository interface {
	RateFor(ctx context.Context, status string) (float64, error)
}

type Audit interface {
	Record(ctx context.Context, userID string, amount float64) error
}

// DiscountService is the subject: it must receive the very doubles the test
// arranges.
type DiscountService struct {
	Users UserRepository
	Rates RateRepository
	Audit Audit
}

type Result struct {
	UserID   string
	Discount float64
}

func (s *DiscountService) Calculate(ctx context.Context, userID string, total float64) (Result, error) {
	user, err := s.Users.ByID(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	rate, err := s.Rates.RateFor(ctx, user.Status)
	if err != nil {
		return Result{}, err
	}
	discount := total * rate
	if err := s.Audit.Record(ctx, user.ID, discount); err != nil {
		return Result{}, err
	}

	return Result{UserID: user.ID, Discount: discount}, nil
}
