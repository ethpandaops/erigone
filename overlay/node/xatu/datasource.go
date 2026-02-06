// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

//go:build embedded

package xatu

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/db/rawdb"
	"github.com/erigontech/erigon/execution/protocol"
	erigonstate "github.com/erigontech/erigon/execution/state"
	erigontypes "github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/vm"
	"github.com/erigontech/erigon/execution/vm/evmtypes"
	"github.com/erigontech/erigon/rpc/transactions"
)

// Compile-time check that Service implements DataSource interface.
var _ execution.DataSource = (*Service)(nil)

// BlockNumber returns the current block number.
func (s *Service) BlockNumber(ctx context.Context) (*uint64, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	block, err := s.blockReader.CurrentBlock(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to get current block: %w", err)
	}

	if block == nil {
		return nil, nil
	}

	num := block.NumberU64()

	return &num, nil
}

// BlockByNumber returns the block at the given number.
func (s *Service) BlockByNumber(ctx context.Context, number *big.Int) (execution.Block, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	block, err := s.blockReader.BlockByNumber(ctx, tx, number.Uint64())
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", number, err)
	}

	if block == nil {
		return nil, fmt.Errorf("block %d not found", number)
	}

	return newBlockAdapter(block, s.chainConfig), nil
}

// BlocksByNumbers returns blocks at the given numbers.
// Returns blocks up to the first not-found (contiguous only).
func (s *Service) BlocksByNumbers(ctx context.Context, numbers []*big.Int) ([]execution.Block, error) {
	if len(numbers) == 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	blocks := make([]execution.Block, 0, len(numbers))

	for _, number := range numbers {
		block, err := s.blockReader.BlockByNumber(ctx, tx, number.Uint64())
		if err != nil {
			return nil, fmt.Errorf("failed to get block %d: %w", number, err)
		}

		// Stop at first not-found block (contiguous only)
		if block == nil {
			break
		}

		blocks = append(blocks, newBlockAdapter(block, s.chainConfig))
	}

	return blocks, nil
}

// BlockReceipts returns all receipts for the block at the given number.
func (s *Service) BlockReceipts(ctx context.Context, number *big.Int) ([]execution.Receipt, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	block, err := s.blockReader.BlockByNumber(ctx, tx, number.Uint64())
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", number, err)
	}

	if block == nil {
		return nil, fmt.Errorf("block %d not found", number)
	}

	txNumReader := s.blockReader.TxnumReader()

	receipts, err := rawdb.ReadReceiptsCacheV2(tx, block, txNumReader)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipts for block %d: %w", number, err)
	}

	return adaptReceipts(receipts), nil
}

// TransactionReceipt returns the receipt for the transaction with the given hash.
func (s *Service) TransactionReceipt(ctx context.Context, hash string) (execution.Receipt, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	txHash := common.HexToHash(hash)

	blockNum, txNum, ok, err := s.blockReader.TxnLookup(ctx, tx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup transaction: %w", err)
	}

	if !ok {
		return nil, nil
	}

	block, err := s.blockReader.BlockByNumber(ctx, tx, blockNum)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", blockNum, err)
	}

	if block == nil {
		return nil, fmt.Errorf("block %d not found for transaction %s", blockNum, hash)
	}

	txNumReader := s.blockReader.TxnumReader()

	receipts, err := rawdb.ReadReceiptsCacheV2(tx, block, txNumReader)
	if err != nil {
		return nil, fmt.Errorf("failed to get receipts for block %d: %w", blockNum, err)
	}

	// Calculate txIndex from txNum
	txNumMin, err := txNumReader.Min(ctx, tx, blockNum)
	if err != nil {
		return nil, fmt.Errorf("failed to get min txNum: %w", err)
	}

	if txNumMin+1 > txNum {
		return nil, fmt.Errorf("txNum underflow: txNum=%d, txNumMin=%d", txNum, txNumMin)
	}

	txIndex := int(txNum - txNumMin - 1)
	if txIndex >= len(receipts) {
		return nil, fmt.Errorf("transaction index %d out of range", txIndex)
	}

	return newReceiptAdapter(receipts[txIndex]), nil
}

// DebugTraceTransaction returns the execution trace for the transaction.
func (s *Service) DebugTraceTransaction(
	ctx context.Context,
	hash string,
	blockNumber *big.Int,
	opts execution.TraceOptions,
) (*execution.TraceTransaction, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	txHash := common.HexToHash(hash)

	blockNum, txNum, ok, err := s.blockReader.TxnLookup(ctx, tx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup transaction: %w", err)
	}

	if !ok {
		return nil, fmt.Errorf("transaction %s not found", hash)
	}

	txNumReader := s.blockReader.TxnumReader()

	// Calculate txIndex from txNum
	txNumMin, err := txNumReader.Min(ctx, tx, blockNum)
	if err != nil {
		return nil, fmt.Errorf("failed to get min txNum: %w", err)
	}

	if txNumMin+1 > txNum {
		return nil, fmt.Errorf("txNum underflow: txNum=%d, txNumMin=%d", txNum, txNumMin)
	}

	txIndex := int(txNum - txNumMin - 1)

	// Get block
	block, err := s.blockReader.BlockByNumber(ctx, tx, blockNum)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", blockNum, err)
	}

	if block == nil {
		return nil, fmt.Errorf("block %d not found", blockNum)
	}

	header := block.Header()

	// Compute block context
	statedb, blockCtx, _, chainRules, signer, err := transactions.ComputeBlockContext(
		ctx, s.engine, header, s.chainConfig, s.blockReader, nil, txNumReader, tx, txIndex,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to compute block context: %w", err)
	}

	// Compute tx context
	msg, txCtx, err := transactions.ComputeTxContext(statedb, s.engine, chainRules, signer, block, s.chainConfig, txIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to compute tx context: %w", err)
	}

	// Create structlog tracer
	tracer := NewStructLogTracer(StructLogConfig{
		DisableStorage:   opts.DisableStorage,
		DisableStack:     opts.DisableStack,
		DisableMemory:    opts.DisableMemory,
		EnableReturnData: opts.EnableReturnData,
	})

	// Get the transaction for OnTxStart callback
	txn := block.Transactions()[txIndex]

	// Execute transaction with tracing
	result, err := s.executeWithTracer(statedb, blockCtx, txCtx, msg, tracer, txn)
	if err != nil {
		return nil, fmt.Errorf("failed to execute transaction: %w", err)
	}

	// Build trace result.
	//
	// EIP-7778 note: Erigon's ExecutionResult was split from a single GasUsed field into
	// ReceiptGasUsed (post-refund, what the user pays) and BlockGasUsed (pre-refund, for
	// block gas limit accounting). We use ReceiptGasUsed here because trace.Gas feeds into
	// execution-processor's computeIntrinsicGas() formula, which expects the post-refund
	// receipt gas value. The formula is:
	//   intrinsic = receiptGas - gasCumulative + gasRefund  (uncapped)
	//   intrinsic = receiptGas * 5/4 - gasCumulative        (capped)
	// This remains correct because ReceiptGasUsed preserves the same post-refund semantics
	// that GasUsed had before EIP-7778.
	trace := tracer.GetTraceTransaction()
	trace.Gas = result.ReceiptGasUsed
	trace.Failed = result.Err != nil

	if len(result.ReturnData) > 0 {
		returnValue := common.Bytes2Hex(result.ReturnData)
		trace.ReturnValue = &returnValue
	}

	return trace, nil
}

// ChainID returns the chain ID.
func (s *Service) ChainID() int64 {
	if s.chainConfig.ChainID != nil {
		return s.chainConfig.ChainID.Int64()
	}

	return 1
}

// ClientType returns the client type/version string.
func (s *Service) ClientType() string {
	return "erigon"
}

// IsSynced returns true if the data source is fully synced.
func (s *Service) IsSynced() bool {
	return s.synced.Load()
}

// executeWithTracer executes a transaction with the given tracer.
func (s *Service) executeWithTracer(
	statedb *erigonstate.IntraBlockState,
	blockCtx evmtypes.BlockContext,
	txCtx evmtypes.TxContext,
	msg protocol.Message,
	tracer *StructLogTracer,
	txn erigontypes.Transaction,
) (*evmtypes.ExecutionResult, error) {
	// Set tracer hooks on state
	statedb.SetHooks(tracer.Hooks())

	// Create EVM with tracer
	evm := vm.NewEVM(blockCtx, txCtx, statedb, s.chainConfig, vm.Config{
		Tracer:    tracer.Hooks(),
		NoBaseFee: true,
	})

	// Call OnTxStart to initialize the tracer with the VM context.
	// This is required for the tracer to capture refund values via GetRefund().
	hooks := tracer.Hooks()
	if hooks.OnTxStart != nil {
		hooks.OnTxStart(evm.GetVMContext(), txn, msg.From())
	}

	// Execute
	gp := new(protocol.GasPool).AddGas(msg.Gas()).AddBlobGas(msg.BlobGas())

	result, err := protocol.ApplyMessage(evm, msg, gp, true, false, s.engine)
	if err != nil {
		return nil, err
	}

	return result, nil
}
