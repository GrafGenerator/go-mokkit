module github.com/GrafGenerator/go-mokkit/container/mokkitdo

go 1.27

require (
	github.com/GrafGenerator/go-mokkit v0.0.0
	github.com/samber/do/v2 v2.0.0
)

require github.com/samber/go-type-to-string v1.8.0 // indirect

// Until the first tag: the adapter tracks the core in this repository.
replace github.com/GrafGenerator/go-mokkit => ../..
