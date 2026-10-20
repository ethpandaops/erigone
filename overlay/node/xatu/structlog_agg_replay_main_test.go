//go:build embedded && erigon_main

package xatu

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/ethpandaops/execution-processor/pkg/clickhouse"
	"github.com/ethpandaops/execution-processor/pkg/ethereum"
	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"
	"github.com/ethpandaops/execution-processor/pkg/processor/transaction/structlog_agg"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/common/hexutil"
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/datadir"
	"github.com/erigontech/erigon/db/kv/temporal/temporaltest"
	"github.com/erigontech/erigon/db/state/execctx"
	"github.com/erigontech/erigon/db/state/execctx/execctxapi"
	"github.com/erigontech/erigon/execution/chain"
	chainspec "github.com/erigontech/erigon/execution/chain/spec"
	"github.com/erigontech/erigon/execution/protocol"
	erigonstate "github.com/erigontech/erigon/execution/state"
	"github.com/erigontech/erigon/execution/tests/testutil"
	erigontypes "github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/rpc/rpchelper"
)

const (
	replayFixture = "structlog_agg_replay_fixture.json.gz"

	// replayHeapBound is the peak heap the replay may reach; replayHeapAbort stops the process before it exhausts the host.
	replayHeapBound = 2 << 30
	replayHeapAbort = 8 << 30
)

type replayFixtureDoc struct {
	TransactionIndex int                      `json:"transactionIndex"`
	RawTransaction   string                   `json:"rawTransaction"`
	Header           erigontypes.Header       `json:"header"`
	Prestate         erigontypes.GenesisAlloc `json:"prestate"`
}

// replaySource executes one transaction against its prestate with the embedded StructLogTracer.
type replaySource struct {
	reader erigonstate.StateReader
	header *erigontypes.Header
	txn    erigontypes.Transaction
	cfg    *chain.Config

	structlogs int
	gas        uint64
	failed     bool
}

func (r *replaySource) BlockNumber(context.Context) (*uint64, error) {
	n := r.header.Number.Uint64()

	return &n, nil
}

func (r *replaySource) BlockByNumber(context.Context, *big.Int) (execution.Block, error) {
	return nil, errors.New("not supported by replay")
}

func (r *replaySource) BlocksByNumbers(context.Context, []*big.Int) ([]execution.Block, error) {
	return nil, errors.New("not supported by replay")
}

func (r *replaySource) BlockReceipts(context.Context, *big.Int) ([]execution.Receipt, error) {
	return nil, errors.New("not supported by replay")
}

func (r *replaySource) TransactionReceipt(context.Context, string) (execution.Receipt, error) {
	return nil, errors.New("not supported by replay")
}

func (r *replaySource) ChainID() int64     { return 1 }
func (r *replaySource) ClientType() string { return "erigon-replay" }
func (r *replaySource) IsSynced() bool     { return true }

func (r *replaySource) DebugTraceTransaction(_ context.Context, _ string, _ *big.Int, opts execution.TraceOptions) (*execution.TraceTransaction, error) {
	statedb := erigonstate.New(r.reader)
	blockCtx := protocol.NewEVMBlockContext(r.header, func(uint64) (common.Hash, error) { return common.Hash{}, nil }, nil, accounts.InternAddress(r.header.Coinbase), r.cfg)
	signer := erigontypes.MakeSigner(r.cfg, r.header.Number.Uint64(), r.header.Time)

	msg, err := r.txn.AsMessage(*signer, r.header.BaseFee, blockCtx.Rules(r.cfg))
	if err != nil {
		return nil, err
	}

	msg.SetCheckNonce(false)

	tracer := NewStructLogTracer(StructLogConfig{
		DisableStorage:   opts.DisableStorage,
		DisableStack:     opts.DisableStack,
		DisableMemory:    opts.DisableMemory,
		EnableReturnData: opts.EnableReturnData,
	})

	result, err := (&Service{}).executeWithTracer(statedb, blockCtx, protocol.NewEVMTxContext(msg), msg, tracer, r.txn, r.cfg)
	if err != nil {
		return nil, err
	}

	trace := tracer.GetTraceTransaction()
	trace.Gas = result.ReceiptGasUsed
	trace.Failed = result.Err != nil

	r.structlogs, r.gas, r.failed = len(trace.Structlogs), trace.Gas, trace.Failed

	return trace, nil
}

type replayBlock struct {
	execution.Block

	number *big.Int
}

func (b replayBlock) Number() *big.Int { return b.number }

// TestStructlogAggReplayBoundedHeap replays a mainnet tx whose 385KB return-data buffer stays live for ~469k opcodes.
func TestStructlogAggReplayBoundedHeap(t *testing.T) {
	ctx := context.Background()
	doc := readReplayFixture(t)

	txn, err := erigontypes.DecodeTransaction(hexutil.MustDecode(doc.RawTransaction))
	if err != nil {
		t.Fatal(err)
	}

	db := temporaltest.NewTestDB(t, datadir.New(t.TempDir()))

	tx, err := db.BeginTemporalRw(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	sd, err := execctx.NewSharedDomains(ctx, tx, log.New())
	if err != nil {
		t.Fatal(err)
	}
	defer sd.Close()

	if _, err := testutil.MakePreStateInto(&chain.Rules{}, sd, tx, doc.Prestate, doc.Header.Number.Uint64()); err != nil {
		t.Fatal(err)
	}

	src := &replaySource{
		reader: rpchelper.NewLatestStateReader(sd.AsStateGetter(tx, execctxapi.StateGetterOptions{})),
		header: &doc.Header,
		txn:    txn,
		cfg:    chainspec.Mainnet.Config,
	}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	node := execution.NewEmbeddedNode(logger, "replay", src)
	pool := ethereum.NewPoolWithNodes(logger, "replay", []execution.Node{node}, nil)
	pool.Start(ctx)

	if err := node.MarkReady(ctx); err != nil {
		t.Fatal(err)
	}

	processor, err := structlog_agg.New(&structlog_agg.Dependencies{
		Log:     logger,
		Pool:    pool,
		Network: &ethereum.Network{Name: "mainnet"},
	}, &structlog_agg.Config{
		Config:  clickhouse.Config{Addr: "127.0.0.1:1"},
		Enabled: true,
		Table:   "replay",
	})
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	peak := watchReplayHeap(stop)

	signer := erigontypes.MakeSigner(src.cfg, doc.Header.Number.Uint64(), doc.Header.Time)
	_, err = processor.ProcessTransaction(ctx, replayBlock{number: doc.Header.Number.ToBig()}, doc.TransactionIndex, newTransactionAdapter(txn, signer))

	close(stop)

	// No row buffer is running, so the insert after aggregation is the expected failure.
	if err == nil || !strings.Contains(err.Error(), "insert call frames") {
		t.Fatalf("expected the aggregation to finish and the insert to fail, got %v", err)
	}

	if src.structlogs != 1044812 || src.gas != 11409124 || src.failed {
		t.Fatalf("replay diverged from mainnet: structlogs=%d gas=%d failed=%v", src.structlogs, src.gas, src.failed)
	}

	if p := peak.Load(); p > replayHeapBound {
		t.Fatalf("peak heap %d bytes, want <= %d", p, replayHeapBound)
	}

	t.Logf("peak heap %.2f GB", float64(peak.Load())/(1<<30))
}

func readReplayFixture(t *testing.T) *replayFixtureDoc {
	t.Helper()

	f, err := os.Open(replayFixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	var doc replayFixtureDoc
	if err := json.NewDecoder(zr).Decode(&doc); err != nil {
		t.Fatal(err)
	}

	return &doc
}

func watchReplayHeap(stop <-chan struct{}) *atomic.Uint64 {
	var peak atomic.Uint64

	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)

			if ms.HeapAlloc > peak.Load() {
				peak.Store(ms.HeapAlloc)
			}

			if ms.HeapAlloc > replayHeapAbort {
				fmt.Fprintf(os.Stderr, "FAIL: TestStructlogAggReplayBoundedHeap: heap reached %d bytes, aborting\n", ms.HeapAlloc)
				os.Exit(1)
			}
		}
	}()

	return &peak
}
