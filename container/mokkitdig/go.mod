module github.com/GrafGenerator/go-mokkit/container/mokkitdig

go 1.27

require (
	github.com/GrafGenerator/go-mokkit v0.0.0
	go.uber.org/dig v1.19.0
)

// Until the first tag: the adapter tracks the core in this repository.
replace github.com/GrafGenerator/go-mokkit => ../..
