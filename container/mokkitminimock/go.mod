module github.com/GrafGenerator/go-mokkit/container/mokkitminimock

go 1.27

// Until the first tag: the adapter tracks the core in this repository.
replace github.com/GrafGenerator/go-mokkit => ../..

require (
	github.com/GrafGenerator/go-mokkit v0.0.0-00010101000000-000000000000
	github.com/gojuno/minimock/v3 v3.4.7
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
)
