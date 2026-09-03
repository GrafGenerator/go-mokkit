// A port of ClientCacheServiceTests.cs from the C# example's unit suite.
// Read the two side by side: the same five scenarios, the same vocabulary, the
// same three blocks per test.
package cache_test

import (
	"context"
	"testing"

	"github.com/GrafGenerator/go-mokkit"
	"github.com/GrafGenerator/go-mokkit/example/clients"
)

func TestGetClient_WhenCached_ReturnsDeserializedClient(t *testing.T) {
	f := newFixture(t)

	// ARRANGE — one client and no role to give it, so the verb hands it back.
	// This is the default shape: no sink, no token, no variable above the
	// chain waiting to be filled.
	client := f.Arrange().ACachedClient()

	// ACT
	result := f.Act().GetClient(client.ID)

	// INSPECT
	f.Inspect().
		RetrievedClientMatching(result, client).
		CacheQueried(client.ID)
}

func TestGetClient_WhenMiss_ReturnsNothing(t *testing.T) {
	f := newFixture(t)

	// ARRANGE
	clientID := "client-absent"
	f.Arrange().CacheHasNoClient()

	// ACT
	result := f.Act().GetClient(clientID)

	// INSPECT
	f.Inspect().
		RetrievedNothing(result).
		CacheQueried(clientID)
}

func TestGetClient_WhenCacheFails_DegradesToNothing(t *testing.T) {
	f := newFixture(t)

	// ARRANGE
	f.Arrange().CacheReadFails()

	// ACT
	result := f.Act().GetClient("client-anything")

	// INSPECT
	f.Inspect().RetrievedNothing(result)
}

func TestGetClient_WhenTheRequestIsCancelled_DegradesToNothing(t *testing.T) {
	f := newFixture(t)

	// ARRANGE — the client is in the cache, so nothing but the cancellation
	// can stop the read from finding it.
	client := f.Arrange().ACachedClient()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// ACT — WithContext mutates the chain and returns it, so every verb after
	// it in the chain runs under ctx. The forwarder on Act is what keeps the
	// call fluent.
	result := f.Act().WithContext(ctx).GetClient(client.ID)

	// INSPECT — the subject asked, was refused, and reported a miss rather
	// than an error.
	f.Inspect().
		RetrievedNothing(result).
		CacheQueried(client.ID)
}

func TestSetClient_SerializesAndStoresWithExpiry(t *testing.T) {
	f := newFixture(t)

	// ARRANGE — a returning verb is still a verb: the chain can arrange
	// whatever it likes before the one that hands the artifact back.
	client := f.Arrange().
		CacheIsReachable().
		AClient(WithName("Acme Corporation"))

	// ACT
	f.Act().StoreClient(client)

	// INSPECT
	f.Inspect().CacheStored(client)
}

func TestRemoveClient_RemovesKey(t *testing.T) {
	f := newFixture(t)

	// ARRANGE
	clientID := "client-to-evict"
	f.Arrange().CacheIsReachable()

	// ACT
	f.Act().RemoveClient(clientID)

	// INSPECT
	f.Inspect().
		CacheRemoved(clientID).
		NothingStored()
}

// Kept and Evicted are roles. A token is a type that declares what it names, so
// the verb that produces a client and the verb that reads it agree on which one
// they mean — which is what the return form above cannot express once there are
// two of the same thing in play.
type (
	Kept struct {
		mokkit.Artifact[clients.Client]
	}
	Evicted struct {
		mokkit.Artifact[clients.Client]
	}
)

func TestRemoveClient_EvictsOnlyTheKeyItWasGiven(t *testing.T) {
	f := newFixture(t)

	// ARRANGE — two clients, each named by its role rather than handed back,
	// so the chain stays whole. The second is arranged through And, which is
	// how vocabulary authored as a plain function joins a chain; being generic
	// over the token, it keeps its role across the join.
	f.Arrange().
		CacheHasClient[Kept](WithID("client-kept"), WithName("Acme Corporation")).
		And(cachedClient[Evicted](f, WithID("client-evicted")))

	// ACT
	f.Act().RemoveClient(f.Of[Evicted]().ID)

	// INSPECT
	f.Inspect().
		CacheRemoved(f.Of[Evicted]().ID).
		CacheStillHas(f.Of[Kept]())
}
