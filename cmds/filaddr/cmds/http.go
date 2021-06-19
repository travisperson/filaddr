package cmds

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "net/http/pprof"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/feeds"
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
	svrShutdownTimeout = 1 * time.Second
	ctxCancelWait      = 3 * time.Second
)

var utcLoc *time.Location

func init() {
	loc, err := time.LoadLocation("UTC")
	if err != nil {
		panic(err)
	}

	utcLoc = loc
}

//go:embed addr.tmpl
var htmlTmplAddr string

//go:embed index.tmpl
var htmlTmplIndex string

type versionKey struct{}

type server struct {
	rdb            *redis.Client
	ctx            context.Context
	externalRouter *mux.Router
	internalRouter *mux.Router
}

func (s *server) externalRoutes(host, static string) {
	r := s.externalRouter.Host(host).Subrouter()
	r.Path("/{addr:[f][0-3][a-zA-Z0-9]+}{format:(?:\\.[a-z]+)?}").HandlerFunc(s.handleAddr()).Name("addr")
	r.Path("/redirect").HandlerFunc(s.handleRedirect()).Queries("addr", "{addr:[f][0-3][a-zA-Z0-9]+}")
	r.Path("/").HandlerFunc(s.handleIndex()).Name("index")
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir(static))))
	r.NotFoundHandler = s.handle404()
	err := s.externalRouter.Walk(func(route *mux.Route, router *mux.Router, ancestors []*mux.Route) error {
		pathTemplate, err := route.GetPathTemplate()
		if err == nil {
			logging.Logger.Infow("route template", "path", pathTemplate)
		}
		pathRegexp, err := route.GetPathRegexp()
		if err == nil {
			logging.Logger.Infow("route regexp", "path", pathRegexp)
		}
		queriesTemplates, err := route.GetQueriesTemplates()
		if err == nil {
			logging.Logger.Infow("queries templates", "queries", strings.Join(queriesTemplates, ","))
		}
		queriesRegexps, err := route.GetQueriesRegexp()
		if err == nil {
			logging.Logger.Infow("queries regex", "queries", strings.Join(queriesRegexps, ","))
		}
		methods, err := route.GetMethods()
		if err == nil {
			logging.Logger.Infow("method", "queries", strings.Join(methods, ","))
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}

func (s *server) handle404() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		url, err := s.externalRouter.Get("index").URL()
		if err != nil {
			panic(err)
		}

		http.Redirect(w, r, url.String(), http.StatusTemporaryRedirect)
	}
}

func (s *server) handleRedirect() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		addr, present := query["addr"]
		if !present || len(addr) == 0 {
			panic("Nothing here")
		}

		url, err := s.externalRouter.Get("addr").URL("addr", addr[0], "format", "")
		if err != nil {
			panic(err)
		}

		http.Redirect(w, r, url.String(), http.StatusTemporaryRedirect)
	}
}

func (s *server) handleIndex() http.HandlerFunc {
	tmpl, err := template.New("addr").Parse(htmlTmplIndex)
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if err := tmpl.Execute(w, nil); err != nil {
			logging.Logger.Errorw("failed while writing response", "err", err)
		}
	}
}

func (s *server) handleAddr() http.HandlerFunc {
	type tmplMessage struct {
		Nonce    uint64
		CID      string
		To       string
		Value    string
		ValueRaw string
		DateTime string
	}
	type tpmlFeed struct {
		Addr     string
		RssFeed  string
		AtomFeed string
		Messages []tmplMessage
	}

	type jsonFeed struct {
		Messages []store.MessageRecord
	}

	funcs := template.FuncMap{
		"ellipsis_start": func(addr string) string {
			return addr[:8]
		},
		"ellipsis_middle": func(addr string) string {
			return addr[8 : len(addr)-8]
		},
		"ellipsis_end": func(addr string) string {
			return addr[len(addr)-8:]
		},
	}

	tmpl, err := template.New("addr").Funcs(funcs).Parse(htmlTmplAddr)
	if err != nil {
		panic(err)
	}

	return func(w http.ResponseWriter, r *http.Request) {
		reqCtx, reqCancelFunc := context.WithTimeout(s.ctx, routeTimeout)
		defer reqCancelFunc()
		vars := mux.Vars(r)

		addr, err := address.NewFromString(vars["addr"])
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodGet:
			br := s.rdb.HExists(reqCtx, store.TrackingAddrKey, addr.String())
			if !br.Val() {
				s.rdb.HSet(reqCtx, store.TrackingAddrKey, addr.String(), 0)
				s.rdb.Publish(reqCtx, store.TrackingAddrUpdateKey, "update")
			}

			key := store.AddrFeedKey(addr)
			sr := s.rdb.LRange(reqCtx, key, 0, build.FeedLength)
			msgs, err := sr.Result()
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				logging.Logger.Errorw("failed to get feed", "err", err)
				return
			}

			switch vars["format"] {
			case ".html", "":
				fd := tpmlFeed{
					Addr:     addr.String(),
					RssFeed:  fmt.Sprintf("/%s.rss", addr.String()),
					AtomFeed: fmt.Sprintf("/%s.atom", addr.String()),
				}

				for _, msgStr := range msgs {
					msg := store.MessageRecord{}
					json.Unmarshal([]byte(msgStr), &msg)
					value := msg.Message.Value.String()
					mtime := time.Unix(int64(msg.Block.Timestamp), 0).In(utcLoc)
					fValue, err := types.ParseFIL(fmt.Sprintf("%s afil", value))
					if err != nil {
						panic(err)
					}

					fd.Messages = append(fd.Messages, tmplMessage{
						Nonce:    msg.Message.Nonce,
						CID:      msg.MessageCid.String(),
						To:       msg.Message.To.String(),
						Value:    fValue.Short(),
						ValueRaw: value,
						DateTime: mtime.Format(time.RFC3339),
					})
				}

				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)

				if err := tmpl.Execute(w, fd); err != nil {
					logging.Logger.Errorw("failed while writing response", "err", err)
				}
			case ".json":
				fd := jsonFeed{}

				for _, msgStr := range msgs {
					msg := store.MessageRecord{}
					json.Unmarshal([]byte(msgStr), &msg)
					fd.Messages = append(fd.Messages, msg)
				}

				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusOK)

				enc := json.NewEncoder(w)
				if err := enc.Encode(fd); err != nil {
					logging.Logger.Errorw("failed while writing response", "err", err)
				}
			case ".rss", ".atom":
				now := time.Now()
				feed := &feeds.Feed{
					Title:       addr.String(),
					Link:        &feeds.Link{Href: fmt.Sprintf("http://192.168.1.162:8888/%s", addr)},
					Description: fmt.Sprintf("A list of messages sent from %s", addr),
					Created:     now,
				}

				feed.Items = []*feeds.Item{}
				for _, msgStr := range msgs {
					msg := store.MessageRecord{}
					json.Unmarshal([]byte(msgStr), &msg)
					value := msg.Message.Value.String()
					mtime := time.Unix(int64(msg.Block.Timestamp), 0).In(utcLoc)
					toAddr := msg.Message.To.String()
					fValue, err := types.ParseFIL(fmt.Sprintf("%s afil", value))
					if err != nil {
						panic(err)
					}
					feed.Items = append(feed.Items, &feeds.Item{
						Title:       fmt.Sprintf("Sent %s to %s...%s", fValue.Short(), toAddr[:8], toAddr[len(toAddr)-8:]),
						Id:          msg.MessageCid.String(),
						Link:        &feeds.Link{Href: fmt.Sprintf("https://filfox.info/en/message/%s", msg.MessageCid.String())},
						Description: fmt.Sprintf("Nonce %d, To %s, Value %s", msg.Message.Nonce, toAddr, fValue.Short()),
						Content:     fmt.Sprintf("Nonce %d, To %s, Value %s", msg.Message.Nonce, toAddr, fValue.Short()),
						Created:     mtime,
					})
				}

				if vars["format"] == ".atom" {
					atom, err := feed.ToAtom()
					if err != nil {
						panic(err)
					}
					w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(atom))
					return
				}

				if vars["format"] == ".rss" {
					rss, err := feed.ToRss()
					if err != nil {
						panic(err)
					}
					w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(rss))
					return
				}
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
	}
}

func (s *server) internalRoutes() {
}

var cmdHttp = &cli.Command{
	Name:  "http",
	Usage: "control data",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "listen",
			Usage:   "host and port to listen on",
			EnvVars: []string{"FILADDR_LISTEN"},
			Value:   "localhost:8080",
		},
		&cli.StringFlag{
			Name:    "host",
			Usage:   "host and port to listen on",
			EnvVars: []string{"FILADDR_HOST"},
			Value:   "localhost:8080",
		},
		&cli.StringFlag{
			Name:    "internal",
			Usage:   "host and port to listen on",
			EnvVars: []string{"FILADDR_INTERNAL"},
			Value:   "localhost:9090",
		},
		&cli.StringFlag{
			Name:  "static",
			Usage: "location of static assets",
			Value: "./static",
		},
		&cli.StringFlag{
			Name:    "redis",
			Usage:   "redis connection string",
			EnvVars: []string{"FILADDR_REDIS"},
		},
		&cli.StringFlag{
			Name:    "redis-password",
			Usage:   "redis password",
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

		s := server{
			ctx:            ctx,
			internalRouter: mux.NewRouter(),
			externalRouter: mux.NewRouter(),
			rdb:            rdb,
		}

		s.externalRoutes(cctx.String("host"), cctx.String("static"))

		externalHttpServer := http.Server{
			Addr:    cctx.String("listen"),
			Handler: s.externalRouter,
		}

		go func() {
			err := externalHttpServer.ListenAndServe()
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

		s.internalRouter.PathPrefix("/debug/pprof/").Handler(http.DefaultServeMux)

		s.internalRouter.HandleFunc("/liveness", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		ready := true
		readyMu := sync.Mutex{}

		s.internalRouter.HandleFunc("/readiness", func(w http.ResponseWriter, r *http.Request) {
			readyMu.Lock()
			isReady := ready
			readyMu.Unlock()

			if isReady {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})

		internalHttpServer := http.Server{
			Addr:    cctx.String("internal"),
			Handler: s.internalRouter,
		}

		go func() {
			err := internalHttpServer.ListenAndServe()
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
			shutdownChan <- externalHttpServer.Shutdown(ctx)
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

		if err := internalHttpServer.Shutdown(ctx); err != nil {
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
