# go-mokkit

A Go port of [Mokkit](https://mokkit.net). Tests read like the scenario they describe, written in a
vocabulary you author, checked by the compiler — no DSL, no feature files, no runtime binding layer.

```go
func TestGetClient_WhenCached_ReturnsDeserializedClient(t *testing.T) {
	f := newFixture(t)

	client := f.Arrange().CacheHasClient(WithName("Acme Corporation"))

	result := f.Act().GetClient(client.ID)

	f.Inspect().
		RetrievedClientMatching(result, client).
		CacheQueried(client.ID)
}
```

Every name in that test is one you wrote. `CacheHasClient`, `GetClient` and `CacheQueried` are
ordinary Go methods on your own types, so they are autocompleted, renamed, and type-checked like any
other code — and a failure reports the test's own line:

```
client_cache_service_test.go:14: inspect: CacheQueried: the cache was never asked for "client-1"
```

**Requires Go 1.27**, which is the first release where methods may declare type parameters. That is
load-bearing here, not incidental: it is what lets artifacts be reached by a token instead of a
string, and what puts `Resolve` on the host rather than in a free function.

---

## Install

```
go get github.com/GrafGenerator/go-mokkit
```

---

## The three phases

A test has an **Arrange** block, an **Act**, and an **Inspect**. They differ in one way that matters:

| phase | on failure |
| --- | --- |
| Arrange, Act | **hard** — `t.Fatalf`, so the rest of the chain never runs. A broken setup makes every later step meaningless. |
| Inspect | **soft** — `t.Errorf`, and carry on, so one run reports *every* failing observation rather than only the first. |

Chains are **eager**. There is no terminal call and nothing is deferred: by the time a verb returns,
its step has already run. That is why an Act verb can simply return its result, and why a chain can
be broken into several statements whenever that reads better.

---

## Authoring a vocabulary

Declare your own phase types by embedding `*mokkit.Chain`, and hang verbs on them. `gofumpt` wants
the three declarations grouped:

```go
type (
	Arrange struct{ *mokkit.Chain }
	Act     struct{ *mokkit.Chain }
	Inspect struct{ *mokkit.Chain }
)
```

A verb marks itself a helper, adds a named step, and returns the chain so it composes:

```go
func (a Arrange) CacheIsReachable() Arrange {
	a.Helper()
	a.Add("CacheIsReachable", func(ctx context.Context, h mokkit.Host) error {
		h.Resolve[*cacheProbe]().reachable = true

		return nil
	})

	return a
}
```

`a.Helper()` is the first line of every verb. Without it a failure reports the verb's body instead of
the test's line, which makes a suite materially harder to work in.

An **Act** verb returns its artifact directly, because the chain is eager:

```go
func (a Act) GetClient(id string) *clients.Client {
	a.Helper()

	var out *clients.Client
	a.Add("GetClient", func(ctx context.Context, h mokkit.Host) error {
		var err error
		out, err = h.Resolve[*cache.ClientCacheService]().GetClient(ctx, id)

		return err
	})

	return out
}
```

### Where verbs live

One rule, and it is worth enforcing in review: **a scenario file holds tests and
nothing else.** Verbs live in files named for their phase.

```
fixture_test.go     composition, tokens, the fixture. No verbs.
arrange_test.go     Arrange verbs
act_test.go         Act verbs
inspect_test.go     Inspect verbs, and the plain-function Steps And and All take
<feature>_test.go   tests
```

The reason is that a verb in a scenario file is invisible: it reads as part of
the story on first encounter, so the next person writes a second one beside it
rather than reaching for the one that already exists, and the vocabulary stops
compounding. Keeping verbs out of scenario files is what makes "is there already
a verb for this?" a question with an answer.

A suite large enough to want it can split a phase by feature — `arrange_cache_test.go`,
`arrange_billing_test.go` — which is the Go-normal shape and keeps the rule intact.

### Verbs should be atomic

A verb sets up **one** condition and says so in its name. Resist the verb that
arranges a whole working world: it hides which of the things it did the test
actually depends on, and every test that needs a variation grows another
parameter until nobody can tell what a given call sets up.

```go
// Each condition is named, so the branch under test is visible in the test.
f.Arrange().
	ACategoryThatAllowsActivation[Card]().
	AnEmissionRequiringCVC[Card]("4321").
	APlasticCardReadyToActivate[Card]()
```

A test for the refusal path then differs from the success path by exactly one
verb, which is what makes it obvious what is being tested. Verbs that depend on
an earlier one should say so when it is missing — "no category arranged: an
emission belongs to one" beats a foreign-key violation.

### Vocabulary from another package

Go methods must live in their type's package, so verbs on `Arrange` share one package — split across
files, one per feature. Vocabulary from elsewhere is written as a plain function returning a
`mokkit.Step`, and enters through `And`, which keeps the chain unbroken:

```go
func HasClient(id string) mokkit.Step {
	return mokkit.NewStep("cache.HasClient", func(ctx context.Context, h mokkit.Host) error {
		...
	})
}

f.Arrange().
	CacheIsReachable().
	And(cachevocab.HasClient("client-1")).
	RateIs(Vip, 0.15)
```

`And`, `All` and `WithContext` are promoted from the embedded `*Chain` returning `*mokkit.Chain`, so
a call to any of them would end your fluent chain. Re-declare the ones you want fluent — one line
each, written once per suite:

```go
func (a Arrange) And(steps ...mokkit.Step) Arrange { a.Helper(); a.Chain.And(steps...); return a }
func (i Inspect) All(steps ...mokkit.Step) Inspect { i.Helper(); i.Chain.All(steps...); return i }
```

All three have the same shape — call for effect, return the receiver.

### Do not report around the chain

`Chain` deliberately does **not** embed `TB`. A verb that calls `t.Fatalf` directly — or hands the
chain to `require` — reports around the phase machinery: inside an `All` branch it would `Goexit`
the wrong goroutine, lose the `phase: verb:` prefix, and let a fail-fast chain carry on regardless.

Return an error from the step. When you do want an assertion library, hand it `c.TB()`, never the
chain: `assert` suits Inspect's soft failure, `require` suits Arrange's hard one.

---

## Artifacts

A verb often produces something a later verb or assertion needs. There are two ways to hold it, and
a suite mixes them freely.

### The return form — the default for a one-off

The producing verb hands the artifact back, and the test binds it at the point it is created:

```go
client := f.Arrange().AClient(WithName("Acme"))

result := f.Act().GetClient(client.ID)
```

Nothing is declared above, nothing is a pointer, and go-to-definition on `client` lands on the verb
that made it. A producing verb written this way is terminal — its return type ends the chain — which
is exactly why the second form exists.

### Tokens — for named roles, and to keep the chain whole

A **token** is a type that names a role *and* declares what that role stands for:

```go
type (
	Buyer  struct{ mokkit.Artifact[Client] }
	Seller struct{ mokkit.Artifact[Client] }
	Cart   struct{ mokkit.Artifact[Order] }
)
```

One line each, declared once for the suite. The artifact's type is inferred from the token, so every
call site spells only the token:

```go
f.Arrange().
	ClientExists[Buyer](Vip).
	ClientExists[Seller](Regular).
	OrderFor[Cart](f.Of[Buyer](), 100)

discount := f.Act().DiscountFor[Cart]()

f.Inspect().
	DiscountIs(discount, 15).
	All(
		clientQueried[Buyer](f),
		clientNotQueried[Seller](f),
	)
```

`f.New[Buyer]()` is the write side and hands a producing verb its sink; `f.Of[Buyer]()` is the read
side and returns a **value**, usable in any phase. Nothing is declared above the test, and the chain
never breaks.

When the artifact has **identity** — a recording double whose state the Act mutates and the Inspect
observes — read it with `f.Ref[Buyer]()`, which hands back the pointer. A copy would be a different
thing, and the failure would be silent: assertions reading stale state. `Ref` fails as loudly as `Of`
when nothing was arranged, so the guarantee is the same; only the aliasing differs. Prefer `Of`, so
a read-only phase cannot write through what it is observing.

What the compiler checks for you: a misspelt token is `undefined: Byer`; passing a token that names
an `Order` to a verb declared `[K mokkit.Token[Client]]` is a type error; and reading a role that no
verb produced fails loudly, at the test's line, naming what *was* arranged:

```
discount_test.go:23: mokkit: nothing arranged for main_test.Ghost (have: main_test.Buyer, main_test.Seller)
```

The verb side declares the pairing once:

```go
func (a Arrange) ClientExists[K mokkit.Token[Client]](status string) Arrange {
	a.Helper()
	a.Add("ClientExists["+mokkit.NameOf[K]()+"]", func(ctx context.Context, h mokkit.Host) error {
		c := Client{ID: "client-" + mokkit.NameOf[K](), Status: status}
		*a.New[K]() = c
		h.Resolve[*fakeClients]().add(c)

		return nil
	})

	return a
}
```

`mokkit.NameOf[K]()` puts the role in the failure message, which is worth the two extra tokens:

```
discount_test.go:23: arrange: OrderFor[Cart]: the client it was given is unset
```

**Which to use.** Reach for the return form when a test has one artifact and no reason to name it.
Reach for tokens when a test has several actors, when the artifact is read in more than one phase,
or when the chain has to stay one sentence. Tokens are static by nature — if you need to pick a role
at run time, that is what the return form is for.

---

## Composition

A **Setup** is composed once and is expensive; a **Stage** is a scope over it, entered per test and
closed when the test ends.

```go
var composition *mokkit.Setup

func TestMain(m *testing.M) {
	mocks := mokkitgomock.New()
	mokkitgomock.Add[clients.DistributedCache](mocks, clients.NewMockDistributedCache)

	app := bag.New()
	bag.Scoped(app, func(r mokkit.Resolver) *cache.ClientCacheService {
		return cache.New(mokkit.Resolve[clients.DistributedCache](r))
	})

	setup, err := mokkit.NewSetup(context.Background(), mocks, app)
	if err != nil {
		panic("composing the cache suite: " + err.Error())
	}
	composition = setup

	m.Run()
}
```

Compose in `TestMain`, not `init` — `gochecknoinits` is common in strict lint configs.

The per-test fixture embeds the stage's tokens, which is what puts `New` and `Of` on the fixture:

```go
type fixture struct {
	*mokkit.Tokens
	stage *mokkit.Stage
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	stage := composition.EnterStage(t)

	return &fixture{Tokens: stage.Tokens(), stage: stage}
}

func (f *fixture) Arrange() Arrange { return Arrange{f.stage.Arrange()} }
func (f *fixture) Act() Act         { return Act{f.stage.Act()} }
func (f *fixture) Inspect() Inspect { return Inspect{f.stage.Inspect()} }
```

### `container/bag` — hand-wired

The primary container, not a fallback: in Go, hand-wiring *is* the idiom.

```go
b := bag.New()

bag.Instance[Clock](b, fixedClock)                  // shared by every stage

bag.Scoped(b, func(mokkit.Resolver) *fakeUsers {    // built once per stage
	return newFakeUsers()
})
bag.Alias[UserRepository, *fakeUsers](b)            // one instance, two keys
```

`Alias` is the shape a double wants: the vocabulary arranges and observes it through its concrete
type, while the subject receives it through the interface. A `Scoped` value implementing `io.Closer`
is closed when the stage ends; an `Instance` is yours to close, and an alias closes nothing, because
it never owned what it handed back.

Factories receive a resolver spanning the whole composition, so a real service is built over doubles
another container registered. That is the entire mock-to-DI bridge, with no ambient state.

### The adapters

The core module has **no dependencies**. Everything that touches a third-party
library is its own nested module under `container/`, so a suite pays only for
what it uses:

| module | adapts | the seam it demonstrates |
| --- | --- | --- |
| `container/bag` | nothing — hand-wiring | scoped lifetimes, aliases, cycle detection (in core) |
| `container/mokkitgomock` | go.uber.org/mock | dual-key mocks, per-stage controller, `Satisfied()` |
| `container/mokkitmockery` | mockery / testify mock | the same shape over testify expectations |
| `container/mokkitminimock` | gojuno/minimock | the same shape; reports at cleanup |
| `container/mokkitdo` | samber/do v2 | a DI-built subject over the stage's doubles, with shutdown hooks |
| `container/mokkitdig` | uber-go/dig | `Bridge`/`Expose`: the mock-to-DI seam both ways |

`report/allure` (stdlib-only, lives in core) writes Allure 2 results from the
Observer seam, so a report reads in the vocabulary the suite was written in.

The gomock adapter in detail — the others follow its shape:

### `container/mokkitgomock` — go.uber.org/mock

```go
mocks := mokkitgomock.New()
mokkitgomock.Add[UserRepository](mocks, NewMockUserRepository)
```

Registers the generated mock under **two** keys: the interface, so the subject resolves it, and the
mock's own type, so vocabulary reaches `EXPECT()`. One `gomock.Controller` per stage, bound to that
stage's test, so expectations are asserted when the test finishes.

Stub with `AnyTimes()` in Arrange and assert interactions in Inspect — either through a captured
value, or with `Times(n)` plus a closing `mokkitgomock.Satisfied()`, which puts the failure on the
test's line while the controller's own cleanup still names the missing call.

---

## Groups

`All` runs its branches concurrently and reports every failure, so one run tells you everything that
is wrong. `Group` makes a branch out of several steps, which run in order and stop at the first
failure:

```go
f.Inspect().All(
	mokkit.Group("db", dbRowExists(id), dbIndexUpdated(id)),
	apiClientMatches(id),
	eventPublished("clients.created", id),
)
```

Branches share nothing, and report by returning an error.

---

## Integration and end-to-end

The Setup/Stage split is what makes an expensive composition reusable. Register the shared resource
with `bag.Instance` and the per-test unit of work with `bag.Scoped`; if the scope implements
`io.Closer`, bag closes it when the stage ends, so a test that does not commit leaves nothing behind.

```go
bag.Instance(b, pool)                               // built once
bag.Scoped(b, func(r mokkit.Resolver) *unitOfWork { // opened per stage
	return begin(mokkit.Resolve[*Pool](r))          // Close() rolls back
})
```

Two rules that are easy to get wrong:

- **An isolation scope must be resolved eagerly.** `bag.Scoped` builds on first resolve, so a test
  that never touches the unit of work never builds it — and therefore never cleans up. Resolve it
  when the stage is entered: `mokkit.Resolve[*unitOfWork](stage)`.
- **Code that opens its own transaction cannot be wrapped.** Rolling back a transaction the test
  supplied undoes everything that went through that handle, but a real handler commits its own. Such
  a suite needs a cleanup step alongside the rollback.

`integration_pattern_test.go` demonstrates the whole shape.

---

## Status

Pre-v1 and unreleased; the API is still moving. `DESIGN.md` records why it looks the way it does.
