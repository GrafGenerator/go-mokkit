module github.com/GrafGenerator/go-mokkit/example

go 1.27

require (
	github.com/GrafGenerator/go-mokkit v0.0.0
	github.com/GrafGenerator/go-mokkit/container/mokkitgomock v0.0.0
	go.uber.org/mock v0.6.0
)

// Until the first tag: the example tracks the repository it lives in.
replace (
	github.com/GrafGenerator/go-mokkit => ..
	github.com/GrafGenerator/go-mokkit/container/mokkitgomock => ../container/mokkitgomock
)
