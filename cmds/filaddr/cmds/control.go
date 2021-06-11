package cmds

import (
	"context"
	"fmt"

	"github.com/go-redis/redis/v8"
	"github.com/urfave/cli/v2"

	"github.com/filecoin-project/go-address"

	"github.com/travisperson/filaddr/build"
	"github.com/travisperson/filaddr/internal/store"
)

var cmdControl = &cli.Command{
	Name:  "control",
	Usage: "control data",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "redis",
			Usage:   "redis connection string",
			Value:   "localhost:6379",
			EnvVars: []string{"FILADDR_REDIS"},
		},
		&cli.StringFlag{
			Name:    "redis-password",
			Usage:   "redis password",
			EnvVars: []string{"FILADDR_REDIS_PASSWORD"},
		},
	},
	Subcommands: []*cli.Command{
		{
			Name:      "track",
			Usage:     "start tracking an address",
			ArgsUsage: "[addr]",
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:  "reload",
					Usage: "issue a reload command",
					Value: false,
				},
			},
			Action: func(cctx *cli.Context) error {
				ctx := context.Background()

				rdb := redis.NewClient(&redis.Options{
					Addr:     cctx.String("redis"),
					Password: cctx.String("redis-password"),
					DB:       0,
				})

				rdb.HSet(ctx, store.TrackingAddrKey, cctx.Args().First(), 0)

				if cctx.Bool("reload") {
					rdb.Publish(ctx, store.TrackingAddrUpdateKey, "update")
				}

				return nil
			},
		},
		{
			Name:      "untrack",
			Usage:     "stop tracking an address",
			ArgsUsage: "[addr]",
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:  "reload",
					Usage: "issue a reload command",
					Value: false,
				},
			},
			Action: func(cctx *cli.Context) error {
				ctx := context.Background()

				rdb := redis.NewClient(&redis.Options{
					Addr:     cctx.String("redis"),
					Password: cctx.String("redis-password"),
					DB:       0,
				})

				rdb.HDel(ctx, store.TrackingAddrKey, cctx.Args().First())

				if cctx.Bool("reload") {
					rdb.Publish(ctx, store.TrackingAddrUpdateKey, "update")
				}

				return nil
			},
		},
		{
			Name:  "list-addrs",
			Usage: "list tracked addresses",
			Action: func(cctx *cli.Context) error {
				ctx := context.Background()

				rdb := redis.NewClient(&redis.Options{
					Addr:     cctx.String("redis"),
					Password: cctx.String("redis-password"),
					DB:       0,
				})

				sr := rdb.HKeys(ctx, store.TrackingAddrKey)
				addrStrs, err := sr.Result()
				if err != nil {
					return err
				}

				for _, addrStr := range addrStrs {
					fmt.Println(addrStr)
				}

				return nil
			},
		},
		{
			Name:      "feed",
			Usage:     "list the current feed for an address",
			ArgsUsage: "[addr]",
			Flags: []cli.Flag{
				&cli.Int64Flag{
					Name:  "count",
					Usage: "number of messages to return",
					Value: build.FeedLength,
				},
			},
			Action: func(cctx *cli.Context) error {
				ctx := context.Background()

				rdb := redis.NewClient(&redis.Options{
					Addr:     cctx.String("redis"),
					Password: cctx.String("redis-password"),
					DB:       0,
				})

				addr, err := address.NewFromString(cctx.Args().First())
				if err != nil {
					return err
				}

				key := store.AddrFeedKey(addr)
				sr := rdb.LRange(ctx, key, 0, cctx.Int64("count"))
				msgs, err := sr.Result()
				if err != nil {
					return err
				}

				for _, msg := range msgs {
					fmt.Println(msg)
				}

				return nil
			},
		},
		{
			Name:  "reload",
			Usage: "issue a reload command",
			Action: func(cctx *cli.Context) error {
				ctx := context.Background()

				rdb := redis.NewClient(&redis.Options{
					Addr:     cctx.String("redis"),
					Password: cctx.String("redis-password"),
					DB:       0,
				})

				rdb.Publish(ctx, store.TrackingAddrUpdateKey, "update")

				return nil
			},
		},
	},
}
