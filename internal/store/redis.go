package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/go-redis/redis/v8"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/lotus/chain/types"

	"github.com/travisperson/filaddr/build"
	"github.com/travisperson/filaddr/internal/logging"
)

const (
	TrackingAddrKey       = "tracking_addrs"
	TrackingAddrUpdateKey = "tracking_addrs_update"
	LastHeightKey         = "last_height"
)

func AddrFeedKey(addr address.Address) string {
	return fmt.Sprintf("list_messages_%s", addr.String())
}

type ActorTracker struct {
	actorsMu sync.Mutex
	actors   map[address.Address][]*types.Message
	rdb      *redis.Client
}

func New(rdb *redis.Client) *ActorTracker {
	at := &ActorTracker{}
	at.actors = make(map[address.Address][]*types.Message)
	at.rdb = rdb

	return at
}

func (at *ActorTracker) RecordMessage(msg *types.Message) bool {
	if _, ok := at.actors[msg.From]; ok {
		at.actors[msg.From] = append(at.actors[msg.From], msg)
		return true
	}

	return false
}

func (at *ActorTracker) Flush(ctx context.Context) error {
	at.actorsMu.Lock()
	defer at.actorsMu.Unlock()

	pipe := at.rdb.Pipeline()

	for addr, msgs := range at.actors {
		key := AddrFeedKey(addr)
		for _, msg := range msgs {
			jbs, err := json.Marshal(msg)
			if err != nil {
				logging.Logger.Errorw("failed to marshal msg", "err", err)
				continue
			}

			logging.Logger.Debugw("updating addr", "addr", addr, "msgs", len(msgs))
			pipe.LPush(ctx, key, jbs)
		}
		pipe.LTrim(ctx, key, 0, build.StoredMessages)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}

	return nil
}

func (at *ActorTracker) Clear() {
	for addr, _ := range at.actors {
		at.actors[addr] = []*types.Message{}
	}
}

func (at *ActorTracker) Len() int {
	return len(at.actors)
}

func (at *ActorTracker) Load(ctx context.Context) (int, error) {
	sr := at.rdb.HKeys(ctx, TrackingAddrKey)
	addrStrs, err := sr.Result()
	if err != nil {
		logging.Logger.Errorw("failed to fetch tracking addresses", "err", err)
	}

	at.actorsMu.Lock()
	defer at.actorsMu.Unlock()

	pipe := at.rdb.Pipeline()

	previousAddrs := make([]address.Address, 0, len(at.actors))
	for k := range at.actors {
		previousAddrs = append(previousAddrs, k)
	}

	actors := make(map[address.Address][]*types.Message)

	for _, addrStr := range addrStrs {
		a, err := address.NewFromString(addrStr)
		if err != nil {
			logging.Logger.Warnw("failed to create address", "err", err, "addr", addrStr)
			continue
		}

		actors[a] = []*types.Message{}
	}

	for _, addr := range previousAddrs {
		if _, ok := actors[addr]; !ok {
			pipe.Del(ctx, AddrFeedKey(addr))
		}
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	at.actors = actors

	return len(actors), nil
}
