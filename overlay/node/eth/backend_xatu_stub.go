//go:build !embedded

package eth

import (
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/db/services"
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/protocol/rules"
	"github.com/erigontech/erigon/node"
	"github.com/erigontech/erigon/rpc"
)

// initXatu is a no-op stub when not built with the embedded tag.
// Xatu integration requires the embedded build tag.
func initXatu(
	_ *node.Node,
	_ kv.TemporalRoDB,
	_ services.FullBlockReader,
	_ *chain.Config,
	_ rules.EngineReader,
	_ string,
	_ log.Logger,
) ([]rpc.API, error) {
	return nil, nil
}
