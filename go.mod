module github.com/coalaura/contain

go 1.27.3

require (
	github.com/coalaura/plain v1.6.6
	github.com/diskfs/go-diskfs v1.9.4
	github.com/goccy/go-yaml v1.19.2
	github.com/urfave/cli/v3 v3.14.0
	golang.org/x/net v0.60.0
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0
	gvisor.dev/gvisor v0.0.0-20261121111754-3eadbee70834
)

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc // indirect
	golang.org/x/time v0.15.0 // indirect
)

// Pending https://github.com/diskfs/go-diskfs/pull/464.
replace github.com/diskfs/go-diskfs => github.com/coalaura/go-diskfs v0.0.0-20261121230315-e47771e0b005
