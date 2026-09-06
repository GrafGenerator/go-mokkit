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

**Requires Go 1.27** — the token accessors and `Host.Resolve` are generic methods.

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
its step has already run. An Act verb returns its result directly, and a chain can be broken into
several statements whenever that reads better.

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

A verb marks itself a helper and runs its step through `mokkit.Do`, which hands the phase back so
the verb is one return:

```go
func (a Arrange) CacheIsReachable() Arrange {
	a.Helper()

	return mokkit.Do(a, func(h mokkit.Host) {
		h.Resolve[*cacheProbe]().reachable = true
	})
}
```

The body is a `func(mokkit.Host)` when it cannot fail, a `func(mokkit.Host) error` when it can, a
`mokkit.StepFunc` when it wants the context as an argument, or a `mokkit.Step` from another
package. The step is named after the verb, so a failure reads `arrange: CacheIsReachable: ...`; a
`Step` keeps its own name.

`a.Helper()` is the first line of every verb. Without it a failure reports the verb's body instead of
the test's line.

An **Act** verb returns its artifact directly, through `Get`. An error fails the chain:

```go
func (a Act) GetClient(id string) *clients.Client {
	a.Helper()

	return a.Get(func(h mokkit.Host) (*clients.Client, error) {
		return h.Resolve[*cache.ClientCacheService]().GetClient(h.Context(), id)
	})
}
```

A test about a refusal wants the error as its artifact. `Try` hands back an `Outcome` instead of
failing on it:

```go
func (a Act) TryGetClient(id string) mokkit.Outcome[*clients.Client] {
	a.Helper()

	return a.Try(func(h mokkit.Host) (*clients.Client, error) {
		return h.Resolve[*cache.ClientCacheService]().GetClient(h.Context(), id)
	})
}

outcome := f.Act().TryGetClient("ghost")

f.Inspect().Refused(outcome, "no such client")
```

`Attempt` is `Try` for an operation whose only outcome is whether it failed: it hands back the
error.

Each of these has an `As` form that takes the step's name — `DoAs`, `GetAs`, `TryAs`, `AttemptAs` —
and a `For` form for a verb generic over a role, which appends the role to the name: `DoFor[K]`,
`GetFor[K]`, `TryFor[K]`, `AttemptFor[K]`.

### Where verbs live

**A scenario file holds tests and nothing else.** Vocabulary lives beside it, in as many files as
its size warrants:

```
fixture_test.go     composition and the fixture. No verbs.
vocabulary_test.go  the verbs, in Arrange, Act and Inspect sections
<feature>_test.go   tests
```

A suite of a handful of tests may keep the fixture and the vocabulary in one `suite_test.go`. A
vocabulary past a few hundred lines splits by phase — `arrange_test.go`, `act_test.go`,
`inspect_test.go` — and a phase splits by feature when it grows again: `arrange_cache_test.go`,
`arrange_billing_test.go`.

### Verbs should be atomic

A verb sets up **one** condition and says so in its name. Do not write a verb
that arranges a whole working world.

```go
// Each condition is named, so the branch under test is visible in the test.
f.Arrange().
	ACategoryThatAllowsActivation[Card]().
	AnEmissionRequiringCVC[Card]("4321").
	APlasticCardReadyToActivate[Card]()
```

A test for a refusal path then differs from the success path by exactly one
verb. A verb that depends on an earlier one reports the missing prerequisite:
"no category arranged: an emission belongs to one".

### Vocabulary from another package

Vocabulary from another package is written as a plain function returning a
`mokkit.Step`, and enters through `And` with the chain unbroken:

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
a call to any of them would end your fluent chain. Re-declare the ones you use — one line each,
written once per suite:

```go
func (a Arrange) And(steps ...mokkit.Step) Arrange { a.Helper(); a.Chain.And(steps...); return a }
func (i Inspect) All(steps ...mokkit.Step) Inspect { i.Helper(); i.Chain.All(steps...); return i }
```

All three have the same shape — call for effect, return the receiver.

### Report through the chain

A step reports by returning an error — never by calling the test's `Fatalf` or
`Errorf` from inside the step. When you want an assertion library, hand it
`c.TB()`, never the chain: `assert` suits Inspect's soft failure, `require`
suits Arrange's hard one.

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
observes — read it with `f.Ref[Buyer]()`, which hands back the pointer. Like `Of`, it fails when
nothing was arranged. Prefer `Of` everywhere else: a value cannot be written through by accident.

What the compiler checks for you: a misspelt token is `undefined: Byer`; passing a token that names
an `Order` to a verb declared `[K mokkit.Token[Client]]` is a type error; and reading a role that no
verb produced fails loudly, at the test's line, naming what *was* arranged:

```
discount_test.go:23: mokkit: nothing arranged for main_test.Ghost (have: main_test.Buyer, main_test.Seller)
```

The verb side declares the pairing once, and `DoFor[K]` puts the role in the step label:

```go
func (a Arrange) ClientExists[K mokkit.Token[Client]](status string) Arrange {
	a.Helper()

	return mokkit.DoFor[K](a, func(h mokkit.Host) {
		c := Client{ID: "client-" + mokkit.NameOf[K](), Status: status}
		*a.New[K]() = c
		h.Resolve[*fakeClients]().add(c)
	})
}
```

The role is what the failure message reports under:

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
closed when the test ends. A **Fixture** is what a test body talks to: the three phases, typed as
the suite's own vocabulary, with `New`, `Of` and `Ref` promoted onto it.

When the subject is cheap to build, compose and enter per test:

```go
type fixture = mokkit.Fixture[Arrange, Act, Inspect]

func newFixture(t *testing.T) *fixture {
	t.Helper()

	b := bag.New()
	bag.Fresh[fakeUsers](b)
	bag.Alias[UserRepository, *fakeUsers](b)
	bag.Scoped(b, func(r mokkit.Resolver) *DiscountService {
		return &DiscountService{Users: mokkit.Resolve[UserRepository](r)}
	})

	return mokkit.Enter[Arrange, Act, Inspect](t, b)
}
```

When the composition is expensive — mocks, a database, a broker — build it once in `TestMain` and
enter it per test:

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

```go
type fixture = mokkit.Fixture[Arrange, Act, Inspect]

func newFixture(t *testing.T) *fixture {
	t.Helper()

	return composition.Enter[Arrange, Act, Inspect](t)
}
```

Compose in `TestMain`, not `init`. `EnterContext` on either form runs the stage's steps under an
explicit context. A suite that wants more on its fixture embeds `*mokkit.Fixture[...]` in its own
struct.

### `container/bag` — hand-wired

The primary container, not a fallback: in Go, hand-wiring *is* the idiom.

```go
b := bag.New()

bag.Instance[Clock](b, fixedClock)                  // shared by every stage

bag.Scoped(b, func(mokkit.Resolver) *fakeUsers {    // built once per stage
	return newFakeUsers()
})
bag.Fresh[fakeRates](b)                             // *fakeRates, new(fakeRates) once per stage
bag.Alias[UserRepository, *fakeUsers](b)            // one instance, two keys
```

`Alias` is the shape a double wants: the vocabulary arranges and observes it through its concrete
type, while the subject receives it through the interface. A `Scoped` value implementing `io.Closer`
is closed when the stage ends; an `Instance` is yours to close, and an alias closes nothing, because
it never owned what it handed back.

Factories receive a resolver spanning the whole composition, so a real service is built over doubles
another container registered — the mock-to-DI bridge.

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
