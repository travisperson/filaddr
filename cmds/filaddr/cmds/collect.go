package cmds

import (
	"container/list"
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	_ "net/http/pprof"

	"github.com/go-redis/redis/v8"
	lru "github.com/hashicorp/golang-lru"
	"github.com/urfave/cli/v2"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"

	"github.com/filecoin-project/lotus/api"
	"github.com/filecoin-project/lotus/api/client"
	lotusStore "github.com/filecoin-project/lotus/chain/store"
	"github.com/filecoin-project/lotus/chain/types"
	cliutil "github.com/filecoin-project/lotus/cli/util"

	"github.com/travisperson/filaddr/build"
	"github.com/travisperson/filaddr/internal/logging"
	"github.com/travisperson/filaddr/internal/store"
)

var (
	tipsetTimeout = 20 * time.Second
)

var cmdCollect = &cli.Command{
	Name:  "collect",
	Usage: "start the collect service",
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
		&cli.StringFlag{
			Name:    "lotus",
			Usage:   "lotus connection string",
			Value:   "wss://api.chain.love",
			EnvVars: []string{"FILADDR_LOTUS"},
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := context.Background()

		go func() {
			logging.Logger.Errorw("debug http server closed", "err", http.ListenAndServe("localhost:6060", nil))
		}()

		ainfo := cliutil.ParseApiInfo(cctx.String("lotus"))

		rdb := redis.NewClient(&redis.Options{
			Addr:     cctx.String("redis"),
			Password: cctx.String("redis-password"),
			DB:       0,
		})

		at := store.New(rdb)

		pubsub := rdb.Subscribe(ctx, store.TrackingAddrUpdateKey)
		pschan := pubsub.Channel()

		go func() {
			// ensure a update occurs immediately
			rdb.Publish(ctx, store.TrackingAddrUpdateKey, "update")

			for {
				select {
				case <-pschan:
					start := time.Now()
					logging.Logger.Infow("reloading tracked addresses")

					count, err := at.Load(ctx)
					if err != nil {
						logging.Logger.Errorw("failed to load actors list", "err", err)
						continue
					}

					logging.Logger.Infow("reloading tracked addresses finished", "elapsed", time.Now().Sub(start).Seconds(), "number", count)
				}
			}
		}()

		darg, err := ainfo.DialArgs("v1")
		if err != nil {
			return err
		}

		api, closer, err := client.NewFullNodeRPCV1(ctx, darg, nil)
		if err != nil {
			return err
		}

		defer closer()

		sr := rdb.Get(ctx, store.LastHeightKey)
		height, err := sr.Int64()
		if err != nil {
			logging.Logger.Warnw("failed to get last height")
		}

		if height == 0 {
			head, err := api.ChainHead(ctx)
			if err != nil {
				return err
			}

			height = int64(head.Height())
		}

		tipsetsCh, err := GetTips(ctx, api, abi.ChainEpoch(height), 1)
		if err != nil {
			return err
		}

		addrCache, err := lru.New2Q(build.AddrCacheSize)
		if err != nil {
			return err
		}

		var avg float64
		var rounds int

		for tipset := range tipsetsCh {
			start := time.Now()
			cids := tipset.Cids()

			ctx, cancel := context.WithDeadline(ctx, time.Now().Add(tipsetTimeout))

			if len(cids) == 0 {
				logging.Logger.Errorw("tipset with zero blocks")
				continue
			}

			tmsgs := make([]store.MessageRecord, 0, 32)
			applied := make(map[address.Address]uint64)

			StateAccountKeyQuick := func(ctx context.Context, addr address.Address, tpk types.TipSetKey) (address.Address, error) {
				if addr.Protocol() == address.ID {
					return addr, nil
				}

				if a, ok := addrCache.Get(addr); ok {
					return a.(address.Address), nil
				}

				raddr, err := api.StateLookupID(ctx, addr, tpk)
				if err != nil {
					return raddr, err
				}

				addrCache.Add(addr, raddr)
				return raddr, err
			}

			selectMsg := func(m *types.Message) (bool, error) {
				if m.Method != 0 {
					return false, nil
				}

				sender, err := StateAccountKeyQuick(ctx, m.From, tipset.Key())
				if err != nil {
					return false, err
				}

				// The first match for a sender is guaranteed to have correct nonce -- the block isn't valid otherwise
				if _, ok := applied[sender]; !ok {
					applied[sender] = m.Nonce
				}

				if applied[sender] != m.Nonce {
					return false, nil
				}

				applied[sender]++

				return true, nil
			}

			for _, cid := range cids {
				msgs, err := api.ChainGetBlockMessages(ctx, cid)
				if err != nil {
					logging.Logger.Errorw("failed to get block messages", "err", err)
					return err
				}

				blockBytes, err := api.ChainReadObj(ctx, cid)
				if err != nil {
					logging.Logger.Errorw("failed to get block bytes", "err", err)
					return err
				}

				block, err := types.DecodeBlock(blockBytes)
				if err != nil {
					logging.Logger.Errorw("failed to decode block", "err", err)
					return err
				}

				for _, msg := range msgs.BlsMessages {
					b, err := selectMsg(msg)
					if err != nil {
						logging.Logger.Errorw("failed to select bls msg", "err", err)
						return err
					}

					if b {
						tmsgs = append(tmsgs, store.MessageRecord{
							Message:    msg,
							MessageCid: msg.Cid(),
							Block:      block,
							TipSetKey:  tipset.Key(),
						})
					}
				}

				for _, msg := range msgs.SecpkMessages {
					b, err := selectMsg(&msg.Message)
					if err != nil {
						logging.Logger.Errorw("failed to select secpk msg", "err", err)
						return err
					}

					if b {
						tmsgs = append(tmsgs, store.MessageRecord{
							Message:    &msg.Message,
							MessageCid: msg.Cid(),
							Block:      block,
							TipSetKey:  tipset.Key(),
						})
					}
				}
			}

			for _, msg := range tmsgs {
				at.RecordMessage(msg)
			}

			if err := at.Flush(ctx); err != nil {
				logging.Logger.Errorw("failed to flush", "err", err)
				return err
			}

			at.Clear()

			elapsed := time.Now().Sub(start).Seconds()
			avg = getAvg(avg, elapsed, rounds)
			rounds = rounds + 1

			logging.Logger.Infow("tipset processed", "height", tipset.Height(), "blocks", len(tipset.Cids()), "msgs", len(tmsgs), "elapsed", elapsed, "avg", avg, "tracking", at.Len(), "cache_size", addrCache.Len())

			rdb.Set(ctx, store.LastHeightKey, int64(tipset.Height()), 0)
			cancel()

		}

		return nil
	},
}

func getAvg(avg, x float64, n int) float64 {
	return (avg*float64(n) + x) / float64(n+1)
}

func GetTips(ctx context.Context, api api.FullNode, lastHeight abi.ChainEpoch, headlag int) (<-chan *types.TipSet, error) {
	chmain := make(chan *types.TipSet)

	hb := newHeadBuffer(headlag)

	notif, err := api.ChainNotify(ctx)
	if err != nil {
		return nil, err
	}

	go func() {
		defer close(chmain)

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		var lastTipset *types.TipSet

		for {
			select {
			case changes := <-notif:
				for _, change := range changes {
					logging.Logger.Debugw("head event", "height", change.Val.Height(), "type", change.Type)

					switch change.Type {
					case lotusStore.HCCurrent:
						tipsets, err := loadTipsets(ctx, api, change.Val, lastHeight)
						if err != nil {
							log.Fatal(err)
							return
						}

						for _, tipset := range tipsets {
							lastTipset = tipset
							chmain <- tipset
						}
					case lotusStore.HCApply:
						if out := hb.push(change); out != nil {
							lastTipset = out.Val
							chmain <- out.Val
						}
					case lotusStore.HCRevert:
						hb.pop()
					}
				}
			case <-ticker.C:
				logging.Logger.Debugw("liveness")

				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)

				if head, err := api.ChainHead(cctx); err != nil {
					logging.Logger.Errorw("failed liveness check", "err", err)
					cancel()
					return
				} else {
					fmt.Printf("head: %v\n", head)
					fmt.Printf("lastTipset: %v\n", lastTipset)
					if head.Height()-lastTipset.Height() > abi.ChainEpoch(2*headlag) {
						logging.Logger.Errorw("notify channel has fallend behind", "head", head.Height(), "last_tipset", lastTipset.Height())
						cancel()
						return
					}
				}

				cancel()
			case <-ctx.Done():
				logging.Logger.Debugw("context canceled")

				return
			}
		}
	}()

	return chmain, nil
}

type headBuffer struct {
	buffer *list.List
	size   int
}

func newHeadBuffer(size int) *headBuffer {
	buffer := list.New()
	buffer.Init()

	return &headBuffer{
		buffer: buffer,
		size:   size,
	}
}

func (h *headBuffer) push(hc *api.HeadChange) (rethc *api.HeadChange) {
	if h.buffer.Len() == h.size {
		var ok bool

		el := h.buffer.Front()
		rethc, ok = el.Value.(*api.HeadChange)
		if !ok {
			panic("Value from list is not the correct type")
		}

		h.buffer.Remove(el)
	}

	h.buffer.PushBack(hc)

	return
}

func (h *headBuffer) pop() {
	el := h.buffer.Back()
	if el != nil {
		h.buffer.Remove(el)
	}
}

func loadTipsets(ctx context.Context, api api.FullNode, curr *types.TipSet, lowestHeight abi.ChainEpoch) ([]*types.TipSet, error) {
	tipsets := []*types.TipSet{}
	for {
		if curr.Height() == 0 {
			break
		}

		if curr.Height() <= lowestHeight {
			break
		}

		tipsets = append(tipsets, curr)

		tsk := curr.Parents()
		prev, err := api.ChainGetTipSet(ctx, tsk)
		if err != nil {
			return tipsets, err
		}

		curr = prev
	}

	for i, j := 0, len(tipsets)-1; i < j; i, j = i+1, j-1 {
		tipsets[i], tipsets[j] = tipsets[j], tipsets[i]
	}

	return tipsets, nil
}
