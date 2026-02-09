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
	"github.com/erigontech/erigon/rpc"
)

// initXatu initializes the Xatu service when built with the embedded tag.
// Returns the APIs to register and any error.
// If xatuConfigPath is "simulation", enables simulation-only mode (no config file needed).
func initXatu(
	stack *node.Node,
	chainKv kv.TemporalRoDB,
	blockReader services.FullBlockReader,
	chainConfig *chain.Config,
	engine rules.EngineReader,
	xatuConfigPath string,
	logger log.Logger,
) ([]rpc.API, error) {
	xatuConfig := xatu.Config{
		ConfigPath:     xatuConfigPath,
		SimulationOnly: xatuConfigPath == "simulation",
	}

	svc, err := xatu.New(stack, chainKv, blockReader, chainConfig, engine, xatuConfig, logger)
	if err != nil {
		return nil, err
	}

	return []rpc.API{
		{
			Namespace: "xatu",
			Service:   svc,
			Public:    true,
		},
	}, nil
}
