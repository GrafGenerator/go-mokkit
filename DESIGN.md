# go-mokkit — design

A Go port of [Mokkit](https://mokkit.net) (C#). Same idea: tests read like the scenario they
describe, written in a vocabulary you author, checked by the compiler — no DSL, no feature files,
no runtime binding layer.

This document records the design decisions and the build order. It is not user documentation.

---

## 1. What carries over, and what doesn't

The C# library is five mechanisms. Go keeps three of them, simplifies one, and deletes one.

| C# | Go | Why |
| --- | --- | --- |
| Stage composed once, entered per test | **Kept** | Maps cleanly onto `TestMain`/package-level setup + `t.Cleanup` |
| `Execute<TService>` in arities 1–4 | **Simplified** to `h.Resolve[T]()` | One generic method replaces 16 overloads (a free function until Go 1.27 allowed generic methods) |
| Deferred Arrange/Act/Inspect chains + `await` | **Eager chains, no terminal call** | No `await` to hang deferral on; eager execution deletes several concepts (§3, §4) |
| `Capture` / `Trapture` / `Prop` / `Ensure` / `EnsureValue` | **Deleted** | Deferral is gone; a verb returns its artifact, or fills a sink a token hands out (§3) |
| mock→DI bridge via `AsyncLocal` bag | **Deleted** | Go has no ambient DI; hand-wired factories pull from the stage resolver directly |
| `[MokkitCapture]` source generator | **Dropped** | Go struct literals are already terse enough to not need it |

### Language constraints that shaped everything

The port was designed against Go 1.23 and rewritten against **Go 1.27**, which lifted the single
constraint that had shaped the most API:

- **Methods may now declare type parameters** (Go 1.27). This is why `Slot[T](reg, name)` became
  `reg.New[K]()`, why `Resolve[T](h)` became `h.Resolve[T]()`, and why an artifact is reached by a
  token rather than a string (§3). Verified: generic methods work on plain types and on generic
  types, and are promoted through struct embedding. **Interface methods still may not** — which is
  why `Resolver` stays an interface and `Host` became a struct.
- **No extension methods.** The user declares a chain type embedding `mokkit.Chain` and hangs verbs
  on it (§2). Verbs from other packages enter through `.And(step...)`.
- **No `out` parameters, no operator overloading.** An artifact is either returned by its verb or
  filled through a sink the token registry hands out (§3). `Trapture`'s implicit conversion has no
  analogue and is gone.
- **Argument evaluation order is only partly specified** — but *function and method calls are*
  ordered left to right. That distinction is load-bearing and was originally got wrong (§3).
- **`t.Fatal` only works on the test goroutine** (it is `runtime.Goexit`). Steps return `error`
  internally; the chain reports on the caller's goroutine. This is also why `Chain` no longer
  embeds `TB` (§2).
## 2. The chain: style B′

A vocabulary package declares its own chain types by embedding `mokkit.Chain`:

```go
package vocab

type Arrange struct{ *mokkit.Chain }

// A non-producing verb returns the chain, so it composes.
func (a Arrange) DiscountRateIs(s UserStatus, rate float64) Arrange {
    a.Add("DiscountRateIs", func(ctx context.Context, h mokkit.Host) error {
        repo := mokkit.Resolve[*MockRateRepository](h)
        repo.EXPECT().RateFor(gomock.Any(), s).Return(rate, nil).AnyTimes()
        return nil
    })
    return a
}

// A producing verb fills a caller-declared sink, and still returns the chain (see §3).
func (a Arrange) UserExists(out *User, opts ...UserOpt) Arrange {
    a.Add("UserExists", func(ctx context.Context, h mokkit.Host) error {
        *out = NewUser(opts...)
        repo := mokkit.Resolve[*MockUserRepository](h)
        repo.EXPECT().GetByID(gomock.Any(), out.ID).Return(*out, nil).AnyTimes()
        return nil
    })
    return a
}
```

`Add` executes the step **immediately**. On failure it calls `t.Fatalf`, which `Goexit`s the test
goroutine — so the remaining links in the chain never run. No terminal `.Run()`, no short-circuit
flag, no error plumbing at the call site:

```go
var user User
f.Arrange().
    UserExists(&user, WithStatus(Vip)).
    DiscountRateIs(Vip, 0.15)

result := f.Act().CalculateDiscount(user, 100)

f.Inspect().
    OkResult(result).
    DiscountAppliedFor(user, 15).
    All(UserRepoQueried(user.ID), RateRepoQueried(Vip))
```

### Cross-package vocabulary

Go methods must live in the declaring type's package, so all verbs on `vocab.Arrange` share one
package (split across files, one per feature — the Go-normal shape). Foreign vocabulary enters
through the escape hatch, which keeps the chain unbroken:

```go
func (c *Chain) And(steps ...mokkit.Step) *Chain

f.Arrange().
    DiscountRateIs(Vip, 0.15).
    And(cachevocab.HasClient(&user)).
    CacheIsWarm()
```

This makes the **core contract a `Step`, and the chain sugar over it** — a bare
`func(...) mokkit.Step` vocabulary works standalone and is consumable by any chain.

A `Step` carries its own name (`mokkit.NewStep("cache.HasClient", fn)`) rather than having one
recovered from the runtime. Deriving it was tried and rejected: the compiler inlines the verb that
built the closure, so `runtime.FuncForPC` reports the step under **its caller's** name — in practice
the test function itself, which is worse than no name at all.

#### Vocabulary types must re-declare the chain-returning methods

`And`, `All` and `WithContext` are promoted from the embedded `*Chain` **returning `*Chain`**, so a
call to any of them ends the fluent chain. Each vocabulary type re-declares the ones it wants fluent
— one line each, written once per suite:

```go
func (a Arrange) And(steps ...mokkit.Step) Arrange { a.Helper(); a.Chain.And(steps...); return a }
func (a Arrange) All(steps ...mokkit.Step) Arrange { a.Helper(); a.Chain.All(steps...); return a }
```

`Add` needs no forwarder: verbs discard its result.

The glue is named `And` rather than `Do` so it reads as a continuation of the sentence rather than
an imperative aside. A vocabulary type re-declares it anyway, so a suite may call its own `Also` or
`Then` instead.

### Every verb must mark itself a helper

`Chain` exposes `Helper()`, so a verb's first line is `a.Helper()`. Without it **every failure
reports the verb's body instead of the test's line**, which makes a suite materially harder to work
in.

| verb does | `Helper` reaches `TB` via | failure reports |
| --- | --- | --- |
| `a.Helper()` | an **embedded** `interface{ Helper() }` | the test's line |
| `a.TB().Helper()` | an accessor | the test's line |
| `a.Helper()` | a **hand-written forwarding method** | the **verb's body** |
| nothing | — | the verb's body |

The third row is the trap, and the rewrite fell into it. `testing.T.Helper` records the frame of its
*immediate caller*. A promoted method from an embedded interface is compiler-generated and elided, so
the caller it records is the verb. A hand-written `func (c *Chain) Helper() { c.tb.Helper() }` is a
real frame, so it records *itself* — and every failure in every suite moves to `chain.go`.

The same rule applies inside `Chain`: `Add`, `And`, `All`, `run` and `fail` each call `c.tb.Helper()`
**directly** rather than `c.Helper()`, because each has to mark its own frame. Marking them through
the forwarder left `fail` unmarked, which stopped attribution at `chain.go` before it ever reached
the verb.

This is invisible to a fake reporter whose `Helper` is a no-op, which is exactly how the regression
reached a production suite before being caught. `TestAVerbMarksItsOwnFrameNotTheChains` records which
function called `Helper` and fails if it is the chain's own frame.

#### `Chain` embedded `TB`, and that was a mistake

The first design embedded `TB` in `Chain`, which cost one call per verb and promoted `Errorf`,
`Fatalf`, `FailNow`, `Failed`, `Name` and `Cleanup` onto every vocabulary type — and §2 actively
recommended handing a vocabulary type to `require`. Measured, that advice broke three contracts at
once. A step under `All` calling `i.Fatalf`:

1. lost its `inspect: VerbName:` prefix and pointed at the verb body — the exact thing the `Helper()`
   discipline exists to prevent;
2. `Goexit`ed only the branch goroutine, so the rest of that branch vanished with no trace;
3. did not fail fast — the chain continued past `All`, in a phase whose whole contract is that it
   stops.

`TB` is now a field with two deliberate exports: `Helper()`, forwarded because every verb needs it,
and `TB()`, for the one legitimate use — handing the reporter to an assertion library. Steps report
by returning an error. The earlier measurement stands: `a.Helper()` and `a.TB().Helper()` attribute
identically, so embedding was buying nine characters per verb and selling the phase machinery.


### Failure semantics differ per phase

- **Arrange / Act — hard fail** (`t.Fatalf`). A broken setup makes every later step meaningless.
- **Inspect — soft fail** (`t.Errorf`, keep going). Idiomatic Go, and it reports *every* failing
  observation in one run instead of only the first. This is what C# needed `Assert.Multiple`
  context scopes for; here it is the default.

---

## 3. Producing artifacts — the `out var` problem

C# declares the artifact inline, mid-chain:

```csharp
await Arrange.UserExists(out var user, WithStatus(Vip)).DiscountRateIs(Vip, 0.15m);
```

Go has no `out var`. Three shapes were built and measured against each other; two shipped.

### The rule that was got wrong: which forms are actually ordered

The first design said same-chain artifact flow **must** use a pointer, citing unspecified operand
order. That is true for a bare variable and **false for an accessor call**. The spec:

> all function calls, method calls, receive operations, and binary logical operations are evaluated
> in lexical left-to-right order

So:

| shape | ordered? |
| --- | --- |
| `UserExists(&u).CacheHas(u)` — `u` is a plain operand | **no** |
| `UserExists(&u).CacheHas(*f.Of[Buyer]())` — a method call | **yes** |
| `x := f.Arrange().AUser(); f.Arrange().CacheHas(x)` — statement boundary | **yes** |

`gc` has always evaluated left to right, so this buys a guarantee rather than fixing an observable
bug. What it buys concretely is the right for the read side to return `T` instead of `*T`, which is
what keeps pointers out of read-only positions.

### The return form — the default for a one-off artifact

Because the chain is **eager**, a producing verb can simply hand its artifact back, and splitting a
chain into statements costs nothing but another `f.Arrange()`:

```go
client := f.Arrange().AClient(WithName("Acme"))

result := f.Act().GetClient(client.ID)
```

Nothing declared above, no pointer, no key, and go-to-definition lands on the verb that made it.
Fully compile-checked. The cost is real and bounded: a producing verb written this way is terminal,
so it cannot compose with a non-producing verb, cannot be an `All` branch, and forces per-chain
configuration like `WithContext` to be repeated.

### Tokens — for named roles, and to keep the chain whole

A **token** is a type that names a role and declares what it stands for:

```go
type (
	Buyer  struct{ mokkit.Artifact[Client] }
	Seller struct{ mokkit.Artifact[Client] }
	Cart   struct{ mokkit.Artifact[Order] }
)
```

`Artifact[T]` carries an unexported `artifact() T`, and `Token[T]` is the constraint that reads it
back. The artifact type is therefore **inferred from the token**, so one type parameter does both
jobs and every call site spells only the role:

```go
f.Arrange().
	ClientExists[Buyer](Vip).
	ClientExists[Seller](Regular).
	OrderFor[Cart](f.Of[Buyer](), 100)

discount := f.Act().DiscountFor[Cart]()
```

`New[K]` is the write side and hands a verb its sink; `Of[K]` is the read side and returns a value.
Verified: inference holds on a *method*, across *package* boundaries (the unexported constraint
method promotes), through fixture embedding, and for a token declared inside a test function.

This is the idea the C# original started from — exchanging a token for an object, with the registry
keyed by the token's type — abandoned there in favour of captures. Go 1.27 is what makes it
expressible: before generic methods, the accessor had to be a free function taking the registry, and
the token bought nothing over a string.

### Why tokens replaced the string-keyed registry

The first design keyed slots by an optional string, and admitted this was the one place a
compile-time design went stringly. It could not be closed with typed constants: an untyped string
constant converts implicitly, so with `type UserRef string`, `f.User("byer")` still compiles.
(Verified.)

A token closes it, and closes more than expected:

| | string key | token |
| --- | --- | --- |
| misspelt role | run time | **compile** (`undefined: Byer`) |
| wrong artifact type at a read site | compile | compile |
| verb accepting a role it should not | — | **compile** (`Token[Client]` rejects a `Cart`) |
| role named in the failure label | no | **yes** (`arrange: OrderFor[Cart]: …`) |
| did-you-mean on an unarranged read | yes | yes |

The last row is why the token is a *type key into one registry* rather than a distinct generated
type per pair. An earlier prototype made every `(role, artifact)` combination its own Go type; that
also compile-checked the role, but killed the `(have: …)` hint, because no two roles shared a type
for the hint to enumerate. Keying one registry by `reflect.TypeFor[K]()` keeps both.

Reading a role no verb produced still fails at run time — it has to, since "nothing arranged" is a
statement about execution, not about types — but it fails loudly, at the test's line, naming what
was arranged:

```
discount_test.go:23: mokkit: nothing arranged for main_test.Ghost (have: main_test.Buyer, main_test.Seller)
```

`Of` reports through `TB.Fatalf`. With a token the only way to reach that branch is a genuine
"nothing arranged", because a typo can no longer get there — which is what makes the hard failure
the right call rather than a `(T, bool)`.

### Value or identity: `Of` and `Ref`

`Of` returns a value, which is what keeps `*T` out of read-only positions. That is right for an
artifact that *is* data — a user, an order, a response.

The trial found the other half. A suite's `operation` double records that it ran (`o.ran = true`,
through a closure the Act handed the subject), and a later Inspect asks whether it did. Hand that
artifact back by value and the Inspect reads a copy taken before the Act mutated the original — an
assertion that silently observes stale state, which is a worse failure than any this design set out
to prevent.

So `Ref[K]` is the read side for an artifact with identity. It fails exactly as loudly as `Of` when
nothing was arranged; only the aliasing differs. `Of` stays the default and the documented one,
because a value cannot be written through by accident.

The alternative was to require every mutable double to carry its state behind a pointer
(`type operation struct{ *opState }`) so copies alias. That works, but it makes the library's
correctness depend on a convention nobody is reminded of, and gets silently wrong when forgotten.

### What tokens cannot do

A token is a type, so roles cannot be computed at run time: there is no table-driven loop over
roles. That is the return form's job — bind the artifact to a loop variable — and it is the reason
both forms ship rather than one.

### No `Capture[T]`

The C# capture family — `Capture`, `Trapture`, `Prop`, `Ensure`, `EnsureValue` — reduces to **zero
library concepts**. Eager execution deletes the placeholder they existed to make safe: by the time a
verb returns, its artifact is written. `EnsureValue`'s guard is recovered by `Of`, which fails when
a role was never produced.

### Rejected alternatives

- **A shared `Scenario`/World struct** every verb reads and writes. The call site stops being
  self-documenting, verbs bind to one suite's struct instead of compounding as reusable vocabulary,
  and it reintroduces "the step assumed a field an earlier step never set". It needs no library
  support either way, so it stays available without being endorsed.
- **Threading the artifact type through the chain** (`ArrangeWith[T]` embedding `Arrange`).
  Re-tested under Go 1.27, since generic methods might have rescued it: they do not. Promoted
  methods still return `Arrange` and drop the type parameter.
- **Closure sinks** — `UserExists(opts, func(u User) { user = u })` is strictly worse than either
  shipped form.
- **Role accessors** (`f.NewBuyer()` / `f.Buyer()` over a string registry). Built and measured: the
  support code came out *larger* than the string-key baseline, and the role string had to be spelled
  twice — once per half of the pair — so the two could drift, at run time. Tokens dominate it.
- **A per-role type pair as the key** (`cell[Buyer, Client]`). Compile-checks the role, but loses
  the did-you-mean hint and misattributes the failure to the helper's line.


## 4. What eager execution deletes

Three C# concepts existed only because the chain was deferred until `await`:

- **Act's three flavors** (void / `Returning<T>` / capture) collapse to one. The verb is concrete,
  so it can name its own return type and just return the value:

  ```go
  func (a Act) CalculateDiscount(u User, total Money) DiscountResult {
      var out DiscountResult
      a.Add("CalculateDiscount", func(ctx context.Context, h mokkit.Host) error {
          svc := mokkit.Resolve[*DiscountService](h)
          var err error
          out, err = svc.Calculate(ctx, u, total)
          return err
      })
      return out
  }
  ```

  Act therefore terminates its chain by returning a value, exactly as `ITestAct<T>` did.

- **The whole capture family** — `Capture`, `Trapture`, `Prop`, `Ensure`, `EnsureValue` all existed
  to make a not-yet-filled placeholder safe to hold and read. Eager execution means there is no such
  placeholder; a `*T` sink is filled by the time the call returns. See §3.

---

## 5. Core API

`mokkit` defines its own `TB` rather than using `testing.TB`, which is sealed (`private()`, still, as
of Go 1.27) and so cannot be implemented by a fake reporter, by mokkit's own tests, or by a
non-standard runner. It also keeps `testing` out of the library's non-test imports.

```go
package mokkit

// TB is the part of *testing.T that mokkit reports through.
type TB interface {
	Helper()
	Name() string
	Cleanup(func())
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	FailNow()
	Failed() bool
}
```

**`TB.Fatalf` must not return.** Core calls it and then returns a zero value it expects nobody to
see — `Stage.EnterStageContext` on a container failure, `Tokens.Of` on an unarranged role. A
reporter that returns from `Fatalf` gets a nil `*Stage` or a zero artifact instead of a `Goexit`.
`testing.T` honours this; a custom reporter must too.

```go
// --- steps -------------------------------------------------------------
type StepFunc func(ctx context.Context, h Host) error

type Step struct {
	Name string
	Run  StepFunc
}

func NewStep(name string, fn StepFunc) Step

// --- resolution --------------------------------------------------------
type Resolver interface {
	TryResolveType(t reflect.Type) (any, bool)
}

// PathResolver is the optional half: a resolver that carries the types
// currently under construction, so a cycle spanning two containers is
// reported rather than deadlocking. Stage implements it and threads the
// path through every Scope that does. See §6.
type PathResolver interface {
	Resolver
	TryResolveTypePath(t reflect.Type, path []reflect.Type) (any, bool)
}

// Host is a struct, not an interface, so a verb can resolve with a method.
// Interface methods still cannot declare type parameters; concrete ones can.
type Host struct{ /* ctx, resolver */ }

func NewHost(ctx context.Context, r Resolver) Host
func (h Host) Context() context.Context
func (h Host) Resolver() Resolver
func (h Host) TryResolveType(t reflect.Type) (any, bool)
func (h Host) Resolve[T any]() T
func (h Host) TryResolve[T any]() (T, bool)

// Still free functions, because Resolver is an interface: a bag factory and
// a fixture both hold one of those, not a Host.
func Resolve[T any](r Resolver) T
func TryResolve[T any](r Resolver) (T, bool)

// ResolveError distinguishes "nothing registered" from "something is
// registered under this type but cannot be used as it", which is a
// container bug rather than a missing registration.
type ResolveError struct {
	Type    reflect.Type
	Present bool
}

// --- artifacts ---------------------------------------------------------
type Artifact[T any] struct{}                  // the phantom a token embeds
type Token[T any] interface{ artifact() T }    // the constraint that reads it back

func NameOf[K any]() string

type Tokens struct{ /* map[reflect.Type]any */ }

func NewTokens(t TB) *Tokens
func (r *Tokens) New[K Token[A], A any]() *A      // write side — the sink
func (r *Tokens) Of[K Token[A], A any]() A        // read side  — a value
func (r *Tokens) Ref[K Token[A], A any]() *A      // read side  — identity (§3)
func (r *Tokens) Declared[K Token[A], A any]() bool

// --- chain -------------------------------------------------------------
type Chain struct {
	*Tokens        // embedded: a verb reaches artifacts as the test does
	/* tb TB — a field, NOT embedded; see §2 */
	/* ex, ctx, phase, mode */
}

func (c *Chain) Helper()
func (c *Chain) TB() TB
func (c *Chain) Add(name string, fn StepFunc) *Chain
func (c *Chain) And(steps ...Step) *Chain
func (c *Chain) All(steps ...Step) *Chain
func (c *Chain) Context() context.Context
func (c *Chain) WithContext(ctx context.Context) *Chain   // mutates, like And/All
func Group(name string, steps ...Step) Step

// --- executor ----------------------------------------------------------
type Executor interface {
	Run(ctx context.Context, fn StepFunc) error
	Close() error
}

// --- stage -------------------------------------------------------------
type Setup struct{ /* built containers */ }

func NewSetup(ctx context.Context, builders ...ContainerBuilder) (*Setup, error)
func (s *Setup) EnterStage(t TB) *Stage
func (s *Setup) EnterStageContext(ctx context.Context, t TB) *Stage

type Stage struct{ /* scopes, executor, tokens, cache */ }

func (s *Stage) ID() string
func (s *Stage) Context() context.Context
func (s *Stage) TB() TB
func (s *Stage) Tokens() *Tokens
func (s *Stage) Host() Host
func (s *Stage) TryResolveType(t reflect.Type) (any, bool)
func (s *Stage) TryResolveTypePath(t reflect.Type, path []reflect.Type) (any, bool)
func (s *Stage) Chain(phase string, mode FailMode) *Chain
func (s *Stage) Arrange() *Chain
func (s *Stage) Act() *Chain
func (s *Stage) Inspect() *Chain
func (s *Stage) Close() error    // idempotent; clears the resolution cache
```

Two deliberate shapes here. `Chain.WithContext` **mutates and returns the receiver**, so a
vocabulary forwarder is written exactly like the `And` and `All` ones — the earlier copy-constructor
version compiled fine in the trained forwarder shape and silently did nothing. And `Stage.Close`
takes the lock it shares with `TryResolveType` and empties the cache, so a stage has a lifetime
instead of outliving its test as a bag of dead instances.

Panic handling: the executor recovers panics inside a step and converts them to an error carrying
the original stack, so `Resolve` can panic freely inside verbs and still produce a clean, attributed
failure. `Goexit` (from a nested `t.Fatal`) passes through untouched. The message always carries a
`panic:` prefix, including when the panicked value is an `error`, because a crash and a returned
failure send a reader to different places.

### Container contract

The C# four-phase build (PreInit → Init → PreBuild → Build) existed almost entirely to let a DI
builder see the mock builder's registrations. With hand-wiring there is nothing to bridge, so it
collapses to two phases:

```go
type ContainerBuilder interface {
	Build(ctx context.Context) (Container, error)
}

type Container interface {
	BeginScope(ctx context.Context, sc StageContext) (Scope, error)
}

type Scope interface {
	Resolver
	Close() error
	// A Scope whose factories resolve their own collaborators should also
	// implement PathResolver, or a cycle crossing into it is invisible.
}

type StageContext struct {
	T        TB
	StageID  string
	Resolver Resolver   // the stage; use lazily, at resolve time only
}
```


## 6. Containers shipped in v0.1

### `container/bag` — hand-wired

The primary container, not a fallback. In Go, hand-wiring *is* the idiom.

```go
b := bag.New()

// A double, reachable under its concrete type for the vocabulary to arrange and
// observe, and under its interface for the subject to receive.
bag.Scoped(b, func(mokkit.Resolver) *fakeUsers { return newFakeUsers() })
bag.Alias[UserRepository, *fakeUsers](b)

bag.Instance[Clock](b, fixedClock)

bag.Scoped(b, func(r mokkit.Resolver) *DiscountService {
    return &DiscountService{Users: mokkit.Resolve[UserRepository](r)}
})
```

`Instance` is shared by every stage; `Scoped` is built at most once per stage, on first resolve, and
closed with it if it implements `io.Closer`. Factories receive a `mokkit.Resolver` spanning the whole
composition — **the entire mock→DI bridge, with no ambient state.**

Two things this forced into the core, both found by writing the container rather than designing it:

- **`StageContext.Resolver`.** A factory has to reach doubles another container registered, so the
  scope needs the stage. It is only safe to use lazily, at resolve time: while `BeginScope` runs, the
  sibling scopes it would reach are still opening.
- **`Stage.TryResolveType` had to become re-entrant.** A factory resolving its collaborators calls
  back into the stage, so holding the cache lock across a scope resolve deadlocks. The lock is now
  taken only around the map, which means a lazily-building scope is responsible for single-flighting
  its own construction. `bag` does that with a per-entry lock, and detects cycles by carrying the
  construction path — checked *before* the lock, so a cycle reports `*nodeA -> *nodeB -> *nodeA`
  rather than deadlocking or overflowing the stack.

### `container/mokkitgomock` — go.uber.org/mock

```go
mocks := mokkitgomock.New()
mokkitgomock.Add[UserRepository](mocks, NewMockUserRepository)
```

Registers the generated mock under **two keys**: the interface type (so the subject's factory
resolves it as `UserRepository`) and the mock's own type (so vocabulary resolves
`*MockUserRepository` to reach `EXPECT()`). `I` must be an interface and `M` must implement it, both
checked at registration so a mismatch fails while the fixture is being written.

One `gomock.Controller` per stage, built at `BeginScope` from `StageContext.T`. `mokkit.TB` already
satisfies gomock's `TestHelper` *and* its cleanup interface, so the controller reports through the
test and finishes when it does, with no glue. Cleanups run last-registered-first and `Stage.Close`
is registered after `BeginScope`, so scopes are released *before* expectations are checked — which
is the order you want.

The package is `mokkitgomock`, not `gomock`, because a fixture needs `gomock.Any()` constantly and a
collision there would be miserable.

#### Where interaction assertions live

This is the one place the doubles change the shape. NSubstitute verifies after the fact
(`repo.Received(1).GetById(id)`), so an interaction assertion is naturally an **Inspect** verb.
gomock declares it up front (`.Times(1)`) and checks it at cleanup — which puts it in **Arrange**,
against the cardinal rule that Arrange produces and Inspect observes.

Measured, the cost is attribution:

| style | failure lands at |
| --- | --- |
| `Times(1)` declared in Arrange | `controller.go`, referencing the **verb's** `EXPECT` line — never the test's |
| captured via `DoAndReturn`, asserted in an Inspect verb | the test's own line, with the phase and verb name |
| `Times(1)` plus `mokkitgomock.Satisfied()` in Inspect | **both** — the test's line, and the controller's detail at cleanup |

`Satisfied()` exists for exactly this. It is an Inspect step over `Controller.Satisfied()`, so a
missing call is reported at the point the chain reaches it:

```
zz_demo_test.go:16: inspect: gomock.Satisfied: expectations declared while arranging have not all been met
controller.go:97:   missing call(s) to *shop.MockUserRepository.ByID(is anything, is equal to u-1 (string)) …:44
```

Guidance: stub with `AnyTimes()` in Arrange, and assert interactions in Inspect — either through a
captured value, or with `Times(n)` plus a closing `Satisfied()`. Both keep the produce/observe split
readable and the failure on the test's line.

---

## 7. Module layout

Single Go module for v0.1:

```
github.com/GrafGenerator/go-mokkit                 package mokkit
├── container/bag                                  package bag
├── container/mokkitgomock                         package mokkitgomock
└── example/                                       worked suite
```

The example stays in the main module rather than getting its own: being compiled and run by
`go test ./...` against the real API is the whole reason to have it.

Adapters with third-party dependencies get split into **nested modules** before v1 so the core
stays dependency-free. Splitting a package into a nested module **does not change its import
path**, so this decision is deferrable at zero cost.

---

## 8. The worked example

`example/` is a port of the C# suite's cache-service unit tests — the same five scenarios, ported to
compare side by side rather than to show the design at its best. It lives in the main module so it
is compiled and run by `go test ./...`, which is the point of having it.

```
example/clients/   the domain and the cache port, plus the generated mock
example/cache/     the subject, and next to it the colocated vocabulary:
                     arrange_cache_test.go · act_cache_test.go · inspect_cache_test.go
                     probe_test.go · clientfaker_test.go · fixture_test.go
                     client_cache_service_test.go   <- the five tests
```

A ported test, whole:

```go
func TestGetClient_WhenCached_ReturnsDeserializedClient(t *testing.T) {
	f := newFixture(t)

	f.Arrange().CacheHasClient(f.NewClient())

	result := f.Act().GetClient(f.Client().ID)

	f.Inspect().
		RetrievedClientMatching(result, f.Client()).
		CacheQueried(f.Client().ID)
}
```

Against the original it is the same shape with less ceremony: no `Capture`, no `.Prop(c => c.Id)`,
no `.EnsureValue`, and the slot registry supplies the declare-at-use-site that `out var` gave C#.

### What the port taught

**Suite helpers are services.** The original's faker is a static class. Here it is registered with
`bag.Scoped`, so each stage gets one seeded identically — deterministic per test, with no static
state, and reachable from any verb through the resolver it already has. The same goes for a fixed
clock or an id generator.

**gomock needs arranging where NSubstitute needed none.** The original's `RemoveClient` test has no
Arrange block at all, because an unstubbed NSubstitute call returns a default. gomock treats an
unexpected call as a failure, so the port adds `CacheIsReachable()`. This is a property of the mock
library, not of mokkit, but any port of a suite will meet it.

**Recording beats declaring, when the assertion belongs in Inspect.** gomock matches a call against
the first expectation that fits, so a stub and a separate recording expectation on the same method
collide. Wiring the mock once to delegate to a probe — which holds both the double's state and its
record of what happened — avoids that, and is what lets `CacheQueried`, `CacheStored` and
`CacheRemoved` stay Inspect verbs rather than becoming `Times(n)` declarations in Arrange. It is the
concrete form of the guidance in §6.

**Act verbs replaced the original's stage reach-through.** The C# tests call the subject through
private `Stage.ExecuteAsync<TSvc, TOut>(...)` helpers rather than Act verbs, because an Act that
returns a value was the heaviest of its three flavours. Here an Act verb is an ordinary method that
returns its artifact, so the operation stays in the vocabulary where the conventions want it. The
reach-through is still available — `Stage` satisfies `Resolver` — but it is no longer the easier path.

---

## 9. Scopes, groups and failure output

### Value and context scopes need no API at all

C# has `ThenValueScope` because its chain was deferred: the value had to be threaded into steps that
had not run yet. Here a scope is just another vocabulary type — one that embeds the chain and carries
the value as a field:

```go
type RetrievedClient struct {
	*mokkit.Chain
	got *clients.Client
}

func (i Inspect) Retrieved(got *clients.Client) RetrievedClient {
	return RetrievedClient{Chain: i.Chain, got: got}
}

func (s RetrievedClient) Named(want string) RetrievedClient { ... }
```

```go
f.Inspect().
	Retrieved(result).
	Found().
	Named("Acme Corporation").
	Active()
```

**Context scopes go the same way.** C# needed `ThenValueScope(value, wrapper)` to run a scope's steps
inside `Assert.Multiple` or a transaction. Soft-failing Inspect covers `Assert.Multiple` outright, and
because the chain is eager, wrapping is ordinary Go:

```go
tx := begin()
defer tx.Rollback()          // runs even when a FailFast step Goexits
f.Inspect().DbRow(id).DbIndex(id)
```

So `ThenValueScope`, `ITestInspectScope`, `ITestInspectScopeWithContext` and `InspectScopeAsyncFn` all
come to nothing in the port.

### All: independence between branches, order within one

`All(steps ...Step)` gives what C#'s branch builders were for — every branch runs and every failure is
reported, so one run tells you everything that is wrong. `Group` makes a branch out of several steps:

```go
f.Inspect().All(
	mokkit.Group("db", dbRowExists(id), dbIndexUpdated(id)),
	apiClientMatches(id),
	eventPublished("clients.created", id),
)
```

Within a group, steps run in order and stop at the first failure — the same as a C# branch, where the
exception ends that branch only. Between branches nothing is shared. Branch *builders* over the
vocabulary type were considered and dropped: branches would share one chain, and a `FailFast` branch
calling `t.Fatal` off the test goroutine would unwind the wrong one.

### Panic output

A panic inside a step is reported against that step, with the stack trimmed to the frames between the
panic and the executor — the vocabulary that panicked and what it called, without the recovery
machinery above it or mokkit's plumbing below. An untrimmed `debug.Stack` buries the two frames that
matter in about twenty that do not:

```
zz_fmt_test.go:14: arrange: cacheIsWarm: panic: mokkit: no service registered as interface { Missing() }
	github.com/GrafGenerator/go-mokkit.Resolve[...]
		/Users/nikita/projects/go-mokkit/resolve.go:32
	github.com/GrafGenerator/go-mokkit_test.TestDemo_PanicFormatting.func1
		/Users/nikita/projects/go-mokkit/zz_fmt_test.go:15
```

---

## 10. Trial on a production service

The port was applied to an untouched service — a `cards` service with 63 test files, no mocking
library, hand-rolled fakes throughout — by rewriting one suite's nine scenarios beside the originals
and leaving both to run. It is lint-clean under that project's `golangci-lint` config (gochecknoinits,
gocritic, gofumpt, nlreturn, perfsprint, revive, and more), and race-clean.

One scenario, before:

```go
func TestExecuteOnceResult_FreshKeyRunsAndStores(t *testing.T) {
	reg, storage := newTestRegistry(t)
	params := IdempotencyOperationParams{Key: "wf:step", TxName: "tx"}

	ran := false
	out, err := reg.ExecuteOnceResult(context.Background(), storage, params,
		func(context.Context, db.StorageTx) (json.RawMessage, error) {
			ran = true
			return json.RawMessage(`{"id":7}`), nil
		})
	if err != nil { t.Fatalf("ExecuteOnceResult: %v", err) }
	if !ran { t.Fatal("fn did not run on a fresh key") }
	if string(out) != `{"id":7}` { t.Fatalf("out = %s, want ...", out) }
	if !storage.store.present["wf:step"] { t.Fatal("completion marker was not stored") }
	if string(storage.store.results["wf:step"]) != `{"id":7}` { t.Fatalf(...) }
}
```

and after:

```go
func TestMokkit_ExecuteOnceResult_FreshKeyRunsAndStores(t *testing.T) {
	f := newFixture(t)

	f.Arrange().
		KeyIsFresh("wf:step").
		AnOperationReturning(f.NewOperation(), `{"id":7}`)

	result := f.Act().ExecuteOnceResult(f.Operation(), "wf:step")

	f.Inspect().
		OperationRan(f.Operation()).
		ResultIs(result, `{"id":7}`).
		MarkerStored("wf:step").
		StoredResultIs("wf:step", `{"id":7}`)
}
```

### What held

**Vocabulary layers onto fakes that already exist.** The suite's `fakeStore` and `fakeStorage` were
registered with `bag.Scoped` unchanged. Adopting mokkit did not mean adopting a mocking library.

**The `ran := false` flag became an artifact.** Every scenario in the original declared a bool, set it
inside a closure, and asserted on it by hand. As an `operation` — produced in Arrange, exercised in
Act, observed in Inspect — it turns into `OperationRan` / `OperationSkipped`, and the pattern
disappears from all nine tests.

**Soft-failing Inspect earns its keep on real tests.** A broken run reports every wrong observation at
its own line, where the original stopped at the first `t.Fatal`:

```
zz_demo_test.go:14: inspect: OperationRan: the operation did not run on a fresh key
zz_demo_test.go:15: inspect: ResultIs: want result {"id":7}, got {"id":42}
zz_demo_test.go:16: inspect: MarkerStored: no completion marker stored for "wf:missing"
zz_demo_test.go:17: inspect: StoredResultIs: want "{\"id\":7}" stored under "wf:step", got "{\"id\":42}"
```

### What the linter changed, and should change in the docs

- **The three-type idiom must be written grouped.** `gofumpt` rejects three consecutive `type X struct{...}`
  declarations. Document it as `type ( Arrange struct{ *mokkit.Chain }; Act ...; Inspect ... )`.
- **Compose in `TestMain`, never `init`.** `gochecknoinits` is common in strict configs; `TestMain` is
  what the worked example already uses.
- **Name a generic act helper distinctly from the function it calls.** `executeOnceWithResult` beside
  the subject's `ExecuteOnceWithResult` trips `revive`'s confusing-naming. `actTypedResult` reads
  better anyway.
- **An act that expects a failure should return the error, not fill a `*error`.** `gocritic`'s
  `ptrToRefParam` pushed this, and it is the better design: the error is the artifact, so the act
  hands it back like any other.

### The honest cost

Test bodies shrank by roughly a third (231 → 155 lines across nine scenarios). Total code grew,
because 543 lines of vocabulary and fixture were added against 386 lines of original test file. Nine
scenarios is below break-even on raw line count: the vocabulary is written once and paid back per
test that reuses it and per reading. A suite this small is the *worst* case for the trade, and it is
worth saying so rather than quoting the flattering number.

---

## 11. Composing for integration and end-to-end

The Setup/Stage split is what makes an expensive composition reusable: a `Setup` is built once and
owns the container, pool or broker; a `Stage` is a scope over it, entered per test and closed after.
`integration_pattern_test.go` demonstrates the whole shape.

**Isolation comes from a scoped unit of work, not from a framework reset.** Register the shared
resource with `bag.Instance` and the per-test scope with `bag.Scoped`; if that scope implements
`io.Closer`, bag closes it when the stage ends, so a test that does not commit leaves nothing behind:

```go
bag.Instance(b, pool)                                  // built once
bag.Scoped(b, func(r mokkit.Resolver) *unitOfWork {    // opened per stage
    return begin(mokkit.Resolve[*Pool](r))             // Close() rolls back
})
```

**Two isolation mechanisms, because code comes in two shapes.** A step that accepts a query handle
*from the test* runs inside the stage's transaction, and rolling that back undoes everything — every
method of the handle works, `Get`, `Select` and `Exec` included. But code that opens its **own**
transaction and commits cannot be wrapped that way, and real handlers do exactly that. Such a suite
needs a cleanup step (truncating the tables it touches) alongside the rollback. Rolling back is still
worth keeping: it is stricter, and it does not depend on remembering every table.

**An isolation scope must be resolved eagerly.** `bag.Scoped` builds on first resolve, so a test that
never touches the unit of work never builds it — and therefore never cleans up after itself.
Isolation cannot depend on what a test happens to use, so the fixture resolves it when the stage is
entered:

```go
stage := composition.EnterStageContext(ctx, t)
mokkit.Resolve[*unitOfWork](stage)   // built now, so its Close is guaranteed to run
```

This is the strongest argument so far for a `bag.ScopedEager` that builds with the scope rather than
on demand.

**Cleanup runs without a context.** `Scope.Close` is `io.Closer`, so a scope that needs a context
during teardown — a database handle whose logger comes off one — has to capture it at construction.
Widening `Close` to take a context would remove that wart at the cost of no longer being the standard
Go interface; it has not been worth it yet.

`testing.T.Context()` does **not** solve this, and makes it sharper: that context is cancelled *just
before* cleanup functions run, so the one context a teardown would reach for is already dead by the
time `Close` is called. Capture at construction remains the answer.

**When the subject's own configuration varies, cache one Setup per configuration** rather than
composing per test. The suites in the trial project compose per test only because their doubles are
free; with a container behind them the cache is what keeps the expensive part paid once. Both
compositions still share the one expensive dependency, because it is registered as an `Instance`.

---

## 12. Build order

| Phase | Deliverable |
| --- | --- |
| 0 | **Done.** `go.mod`, README, `DESIGN.md`, CI. |
| 1 | **Done.** Core: `Step`/`StepFunc`, `Host`, `Resolve`, `TB`, `Chain` (`Add`/`And`/`All`), inline `Executor`, `Setup`/`Stage`, container contract. |
| 2 | **Done.** `container/bag` — `Instance`, `Scoped`, `Alias`, resolver-in-factory, cycle detection, close-with-stage. |
| 3 | **Done.** `container/mokkitgomock` — dual-key registration, per-stage controller bound to the stage's test, `Satisfied()` for Inspect-side attribution. |
| 4 | **Done.** `example/` — the Example1 cache-service unit suite ported whole. See §8. |
| 5 | **Done.** Value and context scopes need no API (§9); `Group` for multi-step `All` branches; trimmed panic stacks. |
| 6 | **Done.** Trial on a production service (§10) and on an integration suite. |
| 7 | **Done.** The Go 1.27 rewrite, driven by what §10 and the review found — see §14. |
| 8 | **Done.** `.golangci.yml` — the trial project's strict config, minus the linters this module has nothing for — plus GitHub Actions running build, vet, `go test -race`, lint and a gofmt check. |

**Deferred, deliberately:** value/context scopes beyond the basics, DI adapters (dig / fx /
samber-do), a testify+mockery adapter, the channel-backed executor, any codegen.


## 13. Open questions

**Resolved in the Go 1.27 rewrite (§14):**

- *Naming: the package name for the gomock adapter.* `mokkitgomock`. `gomockc` was never the name in
  the tree, and the doc said otherwise for months.
- *Whether the per-verb `Helper()` line can be removed.* It cannot — `t.Helper` marks its immediate
  caller — so it is the cost of good attribution, and it is one call, not a design.
- *Whether `Chain` should embed `TB`.* No. §2 records what embedding cost.
- *Whether the slot registry's string key can be closed.* Yes, by a token type. §3.

**Still open:**

- `All` takes `...Step`. C#'s `ThenAll` took *branch builders* (`b => b.ApiClient(id).DbClient(id)`),
  which reads better for multi-step branches. `Group` covers the common case; a generic
  `All[C](c C, branches ...func(C)) C` would read better still, but branches would share one chain.
  Deferred until an e2e suite needs it.
- **Two step currencies.** `Add` takes a `StepFunc`; `And`, `All` and `Group` take a `Step`. So a
  method verb cannot also be an `All` branch without being written twice — once as a
  `func(...) mokkit.Step` and once as a one-line method over it. That two-line pattern is what the
  suites do today. Whether the core should make it one line is unresolved; the honest constraint is
  that an eager verb and a deferred branch are genuinely different things.
- **`Chain` embeds `*Tokens`.** That was chosen so a verb reaches artifacts exactly as a test does.
  It is the same *kind* of widening that embedding `TB` turned out to be, with a much smaller surface
  — two methods, neither of which can report failure — so it ships, on the understanding that it
  reverts to a field and an accessor if it goes the same way.
- **Concurrent cycle detection.** `bag` detects a cycle per construction path, so two goroutines
  entering a genuinely cyclic graph from opposite ends can still meet on each other's entry lock. A
  cyclic graph is a bug either way, and the single-goroutine case now reports legibly across
  containers, so this is documented rather than fixed.
- **`testing/synctest`** (Go 1.25) would let `All` and the executor be tested deterministically
  rather than with real goroutines. Not yet used.

---

## 14. The Go 1.27 rewrite

The port shipped against Go 1.23 and was then tried on a production service (§10) and reviewed. Go
1.27 lifted the constraint that had shaped the most API — methods may declare type parameters — and
the review found four bugs and one bad decision. Both were addressed together, before any release,
which is why none of it is a breaking change to anyone.

### What the review found

| | |
| --- | --- |
| `bag.Alias` registered the aliased value as a closer a **second** time, so a double implementing `io.Closer` was closed twice per stage. | Fixed: an alias is now its own registration kind and owns nothing. |
| A dependency cycle **crossing two containers** deadlocked the test binary to the package timeout. `bag` threaded a construction path, but `Stage.TryResolveType` — the only join between containers — dropped it. | Fixed: `PathResolver`, threaded by `Stage`. |
| `Stage.Close` wrote `s.scopes` outside the mutex guarding every other field. Race-detector confirmed. | Fixed, and `Close` is now idempotent and clears the cache. |
| `panicStack` emitted 60+ frames whenever the stack exceeded its buffer — exactly the case trimming exists for. | Fixed: it grows once, then reports no stack rather than a wall of noise. |
| `Chain.WithContext` was a copy constructor, but the docs grouped it with `And`/`All`. A forwarder written in the trained shape compiled and silently did nothing. | Fixed: it mutates and returns the receiver. |
| `TryResolve` conflated "not registered" with "registered as the wrong type", and `Resolve` then reported the first. | Fixed: `ResolveError.Present`. |

### What changed because of Go 1.27

- `Slot[T](reg, name)` / `Require[T](reg, name)` → `reg.New[K]()` / `reg.Of[K]()`, keyed by a token
  type rather than a string, and read back as a value (§3).
- `Resolve[T](h)` → `h.Resolve[T]()`, which meant `Host` becoming a struct. The free function stays
  for the places that hold a `Resolver` rather than a `Host` — `bag` factories, and fixtures.
- Per-type fixture accessor pairs (`NewClient`/`Client`, one pair per artifact type) are gone
  entirely: `f.New[K]()` and `f.Of[K]()` are promoted from the embedded `*Tokens`.

### The cost of requiring Go 1.27

A test-only library forcing a toolchain bump on its consumers is real friction. Two things soften
it: a module on an older `go` directive can still **call** a generic method — only declaring one
needs 1.27 — and the trial service was already moving. The sharper edge is tooling: an editor,
`gofmt` or a `golangci-lint` pinned below 1.27 reports `method must have no type parameters` on
perfectly valid source, which looks like a compile error and is not.
