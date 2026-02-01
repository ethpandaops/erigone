//go:build embedded

package eth

import (
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/db/services"
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/protocol/rules"
	"github.com/erigontech/erigon/node"
	"github.com/erigontech/erigon/node/xatu"
)

// initXatu initializes the Xatu service when built with the embedded tag.
func initXatu(
	stack *node.Node,
	chainKv kv.TemporalRoDB,
	blockReader services.FullBlockReader,
	chainConfig *chain.Config,
	engine rules.EngineReader,
	xatuConfigPath string,
	logger log.Logger,
) error {
	xatuConfig := xatu.Config{
		ConfigPath: xatuConfigPath,
	}

	return xatu.New(stack, chainKv, blockReader, chainConfig, engine, xatuConfig, logger)
}
