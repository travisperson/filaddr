module github.com/travisperson/filaddr

go 1.16

require (
	contrib.go.opencensus.io/exporter/prometheus v0.3.0 // indirect
	github.com/filecoin-project/go-address v0.0.5
	github.com/filecoin-project/go-state-types v0.1.0
	github.com/filecoin-project/lotus v1.9.0
	github.com/go-redis/redis/v8 v8.10.0
	github.com/gorilla/mux v1.8.0 // indirect
	github.com/urfave/cli/v2 v2.3.0
	go.opencensus.io v0.23.0 // indirect
	go.uber.org/zap v1.16.0
)

replace github.com/filecoin-project/filecoin-ffi => github.com/filecoin-project/ffi-stub v0.1.0
