package cache_test

import "testing"

func TestGetClient_WhenCached_AssertedThroughAValueScope(t *testing.T) {
	f := newFixture(t)

	client := f.Arrange().ACachedClient(WithName("Acme Corporation"))

	result := f.Act().GetClient(client.ID)

	// The scope names the value once; every assertion in the run concerns it.
	f.Inspect().
		Retrieved(result).
		Found().
		Named("Acme Corporation").
		Active()
}

func TestGetClient_WhenMiss_AssertedThroughAValueScope(t *testing.T) {
	f := newFixture(t)

	f.Arrange().CacheHasNoClient()

	result := f.Act().GetClient("client-absent")

	// Because Inspect fails soft, a scope reports every wrong field in one run
	// — which is what C# needed a context scope wrapping Assert.Multiple for.
	f.Inspect().Retrieved(result).Nothing()
}
