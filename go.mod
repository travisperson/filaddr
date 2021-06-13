module github.com/travisperson/filaddr

go 1.16

require (
	contrib.go.opencensus.io/exporter/prometheus v0.3.0 // indirect
	github.com/filecoin-project/go-address v0.0.5
	github.com/filecoin-project/go-amt-ipld/v3 v3.1.0 // indirect
	github.com/filecoin-project/go-hamt-ipld/v3 v3.1.0 // indirect
	github.com/filecoin-project/go-paramfetch v0.0.2-0.20210330140417-936748d3f5ec // indirect
	github.com/filecoin-project/go-state-types v0.1.1-0.20210506134452-99b279731c48
	github.com/filecoin-project/lotus v1.9.0
	github.com/go-redis/redis/v8 v8.10.0
	github.com/gorilla/feeds v1.1.1
	github.com/gorilla/mux v1.8.0
	github.com/hashicorp/golang-lru v0.5.4
	github.com/urfave/cli/v2 v2.3.0
	go.uber.org/zap v1.16.0
)

replace github.com/filecoin-project/filecoin-ffi => github.com/filecoin-project/ffi-stub v0.1.0
