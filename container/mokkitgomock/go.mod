module github.com/GrafGenerator/go-mokkit/container/mokkitgomock

go 1.27

require (
	github.com/GrafGenerator/go-mokkit v0.0.0
	go.uber.org/mock v0.6.0
)

// Until the first tag: the adapter tracks the core in this repository.
replace github.com/GrafGenerator/go-mokkit => ../..
