//go:build embedded && erigon_main

package xatu

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/ethpandaops/execution-processor/pkg/clickhouse"
	"github.com/ethpandaops/execution-processor/pkg/ethereum"
	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"
	"github.com/ethpandaops/execution-processor/pkg/processor/transaction/structlog"
	"github.com/ethpandaops/execution-processor/pkg/processor/transaction/structlog_agg"

	"github.com/erigontech/erigon/common/hexutil"
	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/datadir"
	"github.com/erigontech/erigon/db/kv/temporal/temporaltest"
	"github.com/erigontech/erigon/db/state/execctx"
	"github.com/erigontech/erigon/db/state/execctx/execctxapi"
	"github.com/erigontech/erigon/execution/chain"
	chainspec "github.com/erigontech/erigon/execution/chain/spec"
	"github.com/erigontech/erigon/execution/tests/testutil"
	erigontypes "github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/rpc/rpchelper"
)

const (
	// hoodiCreateReplay is hoodi tx 0x8c246fbf (block 3847147): a CREATE2 and a CREATE, each from a nested call.
	hoodiCreateReplay = "structlog_replay_hoodi_create_fixture.json.gz"
	// hoodiEOACallReplay is hoodi tx 0xa133dd8c (block 3847107): a CALL that sends value to an account without code.
	hoodiEOACallReplay = "structlog_replay_hoodi_eoa_call_fixture.json.gz"
)

// hoodiReplay is a hoodi transaction with its prestate, the addresses its CREATE/CREATE2 frames deployed in
// execution order (from callTracer), and the stack-only structlog trace a Nethermind node returned for it.
type hoodiReplay struct {
	TransactionIndex int                      `json:"transactionIndex"`
	TransactionHash  string                   `json:"transactionHash"`
	RawTransaction   string                   `json:"rawTransaction"`
	Header           erigontypes.Header       `json:"header"`
	Prestate         erigontypes.GenesisAlloc `json:"prestate"`
	CreatedAddresses []string                 `json:"createdAddresses"`
	RPCTrace         json.RawMessage          `json:"rpcTrace"`
}

// rpcTraceSource serves the recorded RPC trace the way the JSON-RPC node decodes debug_traceTransaction.
type rpcTraceSource struct {
	*replaySource

	trace json.RawMessage
}

func (r *rpcTraceSource) DebugTraceTransaction(context.Context, string, *big.Int, execution.TraceOptions) (*execution.TraceTransaction, error) {
	var rsp struct {
		Gas         uint64                `json:"gas"`
		Failed      bool                  `json:"failed"`
		ReturnValue *string               `json:"returnValue"`
		StructLogs  []execution.StructLog `json:"structLogs"`
	}

	if err := json.Unmarshal(r.trace, &rsp); err != nil {
		return nil, err
	}

	if rsp.ReturnValue != nil && (*rsp.ReturnValue == "" || *rsp.ReturnValue == "0x") {
		rsp.ReturnValue = nil
	}

	execution.SanitizeStructLogs(rsp.StructLogs)

	return &execution.TraceTransaction{
		Gas:         rsp.Gas,
		Failed:      rsp.Failed,
		ReturnValue: rsp.ReturnValue,
		Structlogs:  rsp.StructLogs,
	}, nil
}

type replayRows struct {
	structlogs []structlog.Structlog
	frames     []structlog_agg.CallFrameRow
}

// TestStructlogRowsReplayMatchRPC replays hoodi transactions through the StructLogTracer and requires the
// structlog and structlog_agg rows to match those built from the RPC trace of the same transaction.
func TestStructlogRowsReplayMatchRPC(t *testing.T) {
	for _, name := range []string{hoodiCreateReplay, hoodiEOACallReplay} {
		t.Run(name, func(t *testing.T) {
			doc := readHoodiReplay(t, name)
			src := newHoodiReplaySource(t, doc)

			embedded := extractReplayRows(t, src, src.txn, doc)
			rpc := extractReplayRows(t, &rpcTraceSource{replaySource: src, trace: doc.RPCTrace}, src.txn, doc)

			require.False(t, src.failed, "replay failed")
			require.Equal(t, embedded.structlogs[0].TransactionGas, rpc.structlogs[0].TransactionGas, "replay gas")

			for mode, rows := range map[string]replayRows{"embedded": embedded, "rpc": rpc} {
				require.Equal(t, doc.CreatedAddresses, createdAddresses(rows.structlogs), "%s structlog CREATE/CREATE2 call_to_address", mode)
				require.Equal(t, doc.CreatedAddresses, createdFrameTargets(rows.frames), "%s structlog_agg CREATE/CREATE2 target_address", mode)
				requireSyntheticRowsKeyed(t, mode, rows.structlogs)
			}

			requireSameStructlogs(t, embedded.structlogs, rpc.structlogs)
			requireSameFrames(t, embedded.frames, rpc.frames)

			t.Logf("%d structlog rows, %d structlog_agg rows, created %v", len(embedded.structlogs), len(embedded.frames), doc.CreatedAddresses)
		})
	}

	t.Run("synthetic rows present", func(t *testing.T) {
		doc := readHoodiReplay(t, hoodiEOACallReplay)
		src := newHoodiReplaySource(t, doc)
		rows := extractReplayRows(t, src, src.txn, doc)

		synthetic := 0

		for i := range rows.structlogs {
			if rows.structlogs[i].Synthetic {
				synthetic++
			}
		}

		require.Positive(t, synthetic)
	})
}

func newHoodiReplaySource(t *testing.T, doc *hoodiReplay) *replaySource {
	t.Helper()

	ctx := context.Background()

	txn, err := erigontypes.DecodeTransaction(hexutil.MustDecode(doc.RawTransaction))
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(doc.TransactionHash), txn.Hash().Hex())

	db := temporaltest.NewTestDB(t, datadir.New(t.TempDir()))

	tx, err := db.BeginTemporalRw(ctx)
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)

	sd, err := execctx.NewSharedDomains(ctx, tx, log.New())
	require.NoError(t, err)
	t.Cleanup(sd.Close)

	_, err = testutil.MakePreStateInto(&chain.Rules{}, sd, tx, doc.Prestate, doc.Header.Number.Uint64())
	require.NoError(t, err)

	return &replaySource{
		reader: rpchelper.NewLatestStateReader(sd.AsStateGetter(tx, execctxapi.StateGetterOptions{})),
		header: &doc.Header,
		txn:    txn,
		cfg:    chainspec.Hoodi.Config,
	}
}

func extractReplayRows(t *testing.T, src execution.DataSource, txn erigontypes.Transaction, doc *hoodiReplay) replayRows {
	t.Helper()

	ctx := context.Background()

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	node := execution.NewEmbeddedNode(logger, "replay", src)
	pool := ethereum.NewPoolWithNodes(logger, "replay", []execution.Node{node}, nil)
	pool.Start(ctx)
	require.NoError(t, node.MarkReady(ctx))

	network := &ethereum.Network{Name: "hoodi"}
	chConfig := clickhouse.Config{Addr: "127.0.0.1:1"}

	slProcessor, err := structlog.New(&structlog.Dependencies{Log: logger, Pool: pool, Network: network},
		&structlog.Config{Config: chConfig, Enabled: true, Table: "replay"})
	require.NoError(t, err)

	aggProcessor, err := structlog_agg.New(&structlog_agg.Dependencies{Log: logger, Pool: pool, Network: network},
		&structlog_agg.Config{Config: chConfig, Enabled: true, Table: "replay"})
	require.NoError(t, err)

	signer := erigontypes.MakeSigner(chainspec.Hoodi.Config, doc.Header.Number.Uint64(), doc.Header.Time)
	block := replayBlock{number: doc.Header.Number.ToBig()}
	tx := newTransactionAdapter(txn, signer)

	structlogs, err := slProcessor.ExtractStructlogs(ctx, block, doc.TransactionIndex, tx)
	require.NoError(t, err)

	frames, err := aggProcessor.ExtractCallFrames(ctx, block, tx)
	require.NoError(t, err)

	return replayRows{structlogs: structlogs, frames: frames}
}

// createdAddresses lists call_to_address of the CREATE/CREATE2 rows in opcode order.
func createdAddresses(rows []structlog.Structlog) []string {
	sorted := append([]structlog.Structlog(nil), rows...)
	sortStructlogs(sorted)

	out := []string{}

	for i := range sorted {
		if op := sorted[i].Operation; op == "CREATE" || op == "CREATE2" {
			out = append(out, derefOr(sorted[i].CallToAddress, "<nil>"))
		}
	}

	return out
}

// createdFrameTargets lists target_address of the frames entered by CREATE/CREATE2, in frame order.
func createdFrameTargets(frames []structlog_agg.CallFrameRow) []string {
	sorted := append([]structlog_agg.CallFrameRow(nil), frames...)
	sortFrames(sorted)

	out := []string{}

	for i := range sorted {
		f := &sorted[i]
		if f.Operation == "" && (f.CallType == "CREATE" || f.CallType == "CREATE2") {
			out = append(out, derefOr(f.TargetAddress, "<nil>"))
		}
	}

	return out
}

// requireSyntheticRowsKeyed checks each synthetic row has its own (index, synthetic) key and follows a CALL to the same address.
func requireSyntheticRowsKeyed(t *testing.T, mode string, rows []structlog.Structlog) {
	t.Helper()

	type key struct {
		index     uint32
		synthetic bool
	}

	byKey := make(map[key]*structlog.Structlog, len(rows))

	for i := range rows {
		k := key{index: rows[i].Index, synthetic: rows[i].Synthetic}
		require.NotContains(t, byKey, k, "%s: duplicate (index, synthetic) key", mode)

		byKey[k] = &rows[i]
	}

	for k, row := range byKey {
		if !k.synthetic {
			continue
		}

		call, ok := byKey[key{index: k.index}]
		require.True(t, ok, "%s: synthetic row at index %d has no opcode row", mode, k.index)
		require.Equal(t, "CALL", call.Operation, mode)
		require.Empty(t, row.Operation, mode)
		require.Equal(t, call.Depth+1, row.Depth, mode)
		require.Equal(t, call.CallToAddress, row.CallToAddress, mode)
		require.NotEqual(t, call.CallFrameID, row.CallFrameID, mode)
	}
}

// requireSameStructlogs compares rows apart from fields the recorded RPC trace lacks (refund, return data)
// and the 0x prefix the RPC node keeps on the transaction return value.
func requireSameStructlogs(t *testing.T, embedded, rpc []structlog.Structlog) {
	t.Helper()

	require.Len(t, embedded, len(rpc))

	normalize := func(rows []structlog.Structlog) []structlog.Structlog {
		out := append([]structlog.Structlog(nil), rows...)
		sortStructlogs(out)

		for i := range out {
			out[i].UpdatedDateTime = structlog.ClickHouseTime{}
			out[i].Refund = nil
			out[i].ReturnData = nil

			if rv := out[i].TransactionReturnValue; rv != nil {
				trimmed := strings.TrimPrefix(*rv, "0x")
				out[i].TransactionReturnValue = &trimmed
			}
		}

		return out
	}

	a, b := normalize(embedded), normalize(rpc)

	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			t.Fatalf("structlog row %d differs:\nembedded %s\nrpc      %s", i, dumpRow(a[i]), dumpRow(b[i]))
		}
	}
}

// requireSameFrames compares structlog_agg rows apart from the refund the recorded RPC trace lacks and the
// memory columns, which need the memory size only the embedded tracer reports.
func requireSameFrames(t *testing.T, embedded, rpc []structlog_agg.CallFrameRow) {
	t.Helper()

	require.Len(t, embedded, len(rpc))

	a := append([]structlog_agg.CallFrameRow(nil), embedded...)
	b := append([]structlog_agg.CallFrameRow(nil), rpc...)
	sortFrames(a)
	sortFrames(b)

	for i := range a {
		if a[i].GasRefund != b[i].GasRefund {
			require.Zero(t, b[i].GasRefund, "rpc frame %d has a refund", i)

			a[i].GasRefund, a[i].IntrinsicGas, b[i].IntrinsicGas = 0, nil, nil
		}

		rpcMemory := []uint64{b[i].MemWordsSumBefore, b[i].MemWordsSumAfter, b[i].MemWordsSqSumBefore, b[i].MemWordsSqSumAfter, b[i].MemExpansionGas}
		require.Equal(t, []uint64{0, 0, 0, 0, 0}, rpcMemory, "rpc frame %d has memory columns", i)

		a[i].MemWordsSumBefore, a[i].MemWordsSumAfter, a[i].MemWordsSqSumBefore, a[i].MemWordsSqSumAfter, a[i].MemExpansionGas = 0, 0, 0, 0, 0

		if !reflect.DeepEqual(a[i], b[i]) {
			t.Fatalf("structlog_agg row %d differs:\nembedded %s\nrpc      %s", i, dumpRow(a[i]), dumpRow(b[i]))
		}
	}
}

func sortStructlogs(rows []structlog.Structlog) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Index != rows[j].Index {
			return rows[i].Index < rows[j].Index
		}

		return !rows[i].Synthetic && rows[j].Synthetic
	})
}

func sortFrames(frames []structlog_agg.CallFrameRow) {
	sort.SliceStable(frames, func(i, j int) bool {
		if frames[i].CallFrameID != frames[j].CallFrameID {
			return frames[i].CallFrameID < frames[j].CallFrameID
		}

		return frames[i].Operation < frames[j].Operation
	})
}

func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}

	return *s
}

func dumpRow(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%+v", v)
	}

	return string(b)
}

func readHoodiReplay(t *testing.T, name string) *hoodiReplay {
	t.Helper()

	f, err := os.Open(name)
	require.NoError(t, err)

	defer f.Close()

	zr, err := gzip.NewReader(f)
	require.NoError(t, err)

	defer zr.Close()

	var doc hoodiReplay
	require.NoError(t, json.NewDecoder(zr).Decode(&doc))

	return &doc
}
