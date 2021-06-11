package cmds

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/mux"
	"github.com/urfave/cli/v2"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/chain/types"

	"github.com/travisperson/filaddr/build"
	"github.com/travisperson/filaddr/internal/logging"
	"github.com/travisperson/filaddr/internal/store"
)

var (
	routeTimeout       = 5 * time.Second
	svrShutdownTimeout = 10 * time.Second
	ctxCancelWait      = 3 * time.Second
)

type versionKey struct{}

var cmdHttp = &cli.Command{
	Name:  "http",
	Usage: "control data",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "listen",
			Usage: "host and port to listen on",
			Value: "localhost:8080",
		},
		&cli.StringFlag{
			Name:  "internal",
			Usage: "host and port to listen on",
			Value: "localhost:9090",
		},
		&cli.StringFlag{
			Name:    "redis",
			Usage:   "redis connection string",
			EnvVars: []string{"FILADDR_REDIS"},
		},
		&cli.StringFlag{
			Name:    "redis-password",
			Usage:   "redis password",
			Value:   "localhost:6379",
			EnvVars: []string{"FILADDR_REDIS_PASSWORD"},
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx, cancelFunc := context.WithCancel(context.Background())
		ctx = context.WithValue(ctx, versionKey{}, build.Version())

		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)

		rdb := redis.NewClient(&redis.Options{
			Addr:     cctx.String("redis"),
			Password: cctx.String("redis-password"),
			DB:       0,
		})

		mxe := mux.NewRouter()
		mxe.HandleFunc("/{addr}", func(w http.ResponseWriter, r *http.Request) {
			reqCtx, reqCancelFunc := context.WithTimeout(ctx, routeTimeout)
			defer reqCancelFunc()

			vars := mux.Vars(r)

			addr, err := address.NewFromString(vars["addr"])
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			type feed struct {
				Messages []types.Message
			}

			switch r.Method {
			case http.MethodGet:
				br := rdb.HExists(ctx, store.TrackingAddrKey, addr.String())
				if !br.Val() {
					rdb.HSet(ctx, store.TrackingAddrKey, addr.String(), 0)
					rdb.Publish(ctx, store.TrackingAddrUpdateKey, "update")
				}

				key := store.AddrFeedKey(addr)
				sr := rdb.LRange(reqCtx, key, 0, build.FeedLength)
				msgs, err := sr.Result()
				if err != nil {
					w.WriteHeader(http.StatusBadGateway)
					return
				}

				fd := feed{}

				for _, msgStr := range msgs {
					msg := types.Message{}
					json.Unmarshal([]byte(msgStr), &msg)
					fd.Messages = append(fd.Messages, msg)
				}

				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)

				enc := json.NewEncoder(w)
				if err := enc.Encode(fd); err != nil {
					logging.Logger.Errorw("failed while writing response", "err", err)
				}

				return
			case http.MethodOptions:
				w.WriteHeader(http.StatusOK)
				return
			case http.MethodHead:
				w.WriteHeader(http.StatusOK)
				return
			}

			return
		})

		s := http.Server{
			Addr:    cctx.String("listen"),
			Handler: mxe,
		}

		go func() {
			err := s.ListenAndServe()
			switch err {
			case nil:
			case http.ErrServerClosed:
				logging.Logger.Infow("server closed")
			case context.Canceled:
				logging.Logger.Infow("context cancled")
			default:
				logging.Logger.Errorw("error shutting down internal server", "err", err)
			}
		}()

		internalMux := mux.NewRouter()
		internalMux.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)

		internalMux.HandleFunc("/liveness", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		ready := true
		readyMu := sync.Mutex{}

		internalMux.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
			readyMu.Lock()
			isReady := ready
			readyMu.Unlock()

			if isReady {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})

		internalServer := http.Server{
			Addr:    cctx.String("internal"),
			Handler: internalMux,
		}
		go func() {
			err := internalServer.ListenAndServe()
			switch err {
			case nil:
			case http.ErrServerClosed:
				logging.Logger.Infow("server closed")
			case context.Canceled:
				logging.Logger.Infow("context cancled")
			default:
				logging.Logger.Errorw("error shutting down internal server", "err", err)
			}
		}()

		<-signalChan

		readyMu.Lock()
		ready = false
		readyMu.Unlock()

		t := time.NewTimer(svrShutdownTimeout)

		shutdownChan := make(chan error)
		go func() {
			shutdownChan <- s.Shutdown(ctx)
		}()

		select {
		case err := <-shutdownChan:
			if err != nil {
				logging.Logger.Errorw("shutdown finished with an error", "err", err)
			} else {
				logging.Logger.Infow("shutdown finished successfully")
			}
		case <-t.C:
			logging.Logger.Infow("shutdown timed out")
		}

		cancelFunc()
		time.Sleep(ctxCancelWait)

		logging.Logger.Infow("closing down database connections")

		if err := internalServer.Shutdown(ctx); err != nil {
			switch err {
			case nil:
			case http.ErrServerClosed:
				logging.Logger.Infow("server closed")
			case context.Canceled:
				logging.Logger.Infow("context cancled")
			default:
				logging.Logger.Errorw("error shutting down internal server", "err", err)
			}
		}

		logging.Logger.Infow("existing")

		return nil
	},
}
