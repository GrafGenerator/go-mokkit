package cache_test

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/GrafGenerator/go-mokkit/example/clients"
)

// cacheProbe is the double's state and its record of what happened: what the
// cache holds, whether reads fail, and every call the subject made.
//
// The original could stub and verify the same call on one NSubstitute
// substitute. gomock matches a call against the first expectation that fits, so
// a stub and a separate recording expectation on the same method would collide.
// Wiring the mock once to delegate here avoids that, and it is what lets the
// interaction assertions stay in Inspect where the conventions want them.
type cacheProbe struct {
	contents map[string]string
	getErr   error

	gets    []string
	sets    []cacheWrite
	removes []string
}

type cacheWrite struct {
	key   string
	value string
	ttl   time.Duration
}

func (w cacheWrite) String() string { return fmt.Sprintf("%s(ttl=%v)", w.key, w.ttl) }

func newCacheProbe(mock *clients.MockDistributedCache) *cacheProbe {
	probe := &cacheProbe{contents: make(map[string]string)}

	mock.EXPECT().Get(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, key string) (string, error) {
			probe.gets = append(probe.gets, key)
			if probe.getErr != nil {
				return "", probe.getErr
			}

			// Real cache clients honor the caller's context, so the double
			// does too: it is what makes the context a chain runs under
			// observable, and a canceled request is one more way the cache
			// can fail to answer.
			if err := ctx.Err(); err != nil {
				return "", err
			}

			return probe.contents[key], nil
		}).AnyTimes()

	mock.EXPECT().Set(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, key, value string, ttl time.Duration) error {
			probe.sets = append(probe.sets, cacheWrite{key: key, value: value, ttl: ttl})
			probe.contents[key] = value

			return nil
		}).AnyTimes()

	mock.EXPECT().Remove(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, key string) error {
			probe.removes = append(probe.removes, key)
			delete(probe.contents, key)

			return nil
		}).AnyTimes()

	return probe
}

func count(keys []string, want string) int {
	n := 0
	for _, k := range keys {
		if k == want {
			n++
		}
	}

	return n
}
