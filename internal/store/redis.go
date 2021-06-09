package store

import (
	"fmt"

	"github.com/filecoin-project/go-address"
)

const (
	TrackingAddrKey       = "tracking_addrs"
	TrackingAddrUpdateKey = "tracking_addrs_update"
	LastHeightKey         = "last_height"
)

func AddrFeedKey(addr address.Address) string {
	return fmt.Sprintf("list_messages_%s", addr.String())
}
