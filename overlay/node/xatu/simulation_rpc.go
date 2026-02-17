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

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/db/kv"
	"github.com/erigontech/erigon/db/kv/rawdbv3"
	"github.com/erigontech/erigon/execution/protocol"
	"github.com/erigontech/erigon/execution/protocol/fixedgas"
	erigontypes "github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/vm"
	"github.com/erigontech/erigon/rpc/transactions"
)

// SimulateBlockGasRequest is the request for xatu_simulateBlockGas.
type SimulateBlockGasRequest struct {
	BlockNumber uint64             `json:"blockNumber"`
	GasSchedule *CustomGasSchedule `json:"gasSchedule"`
	MaxGasLimit bool               `json:"maxGasLimit"`
}

// BlockGasSummary summarizes gas usage for a block.
type BlockGasSummary struct {
	GasUsed          uint64 `json:"gasUsed"`
	GasLimit         uint64 `json:"gasLimit"`
	WouldExceedLimit bool   `json:"wouldExceedLimit"`
}

// TxSummary summarizes gas impact for a single transaction.
type TxSummary struct {
	Hash             string      `json:"hash"`
	Index            uint64      `json:"index"`
	OriginalStatus   string      `json:"originalStatus"`
	SimulatedStatus  string      `json:"simulatedStatus"`
	OriginalGas      uint64      `json:"originalGas"`
	SimulatedGas     uint64      `json:"simulatedGas"`
	DeltaPercent     float64     `json:"deltaPercent"`
	Diverged         bool        `json:"diverged"`
	OriginalReverts  uint64      `json:"originalReverts"`
	SimulatedReverts uint64      `json:"simulatedReverts"`
	OriginalErrors   []CallError `json:"originalErrors"`
	SimulatedErrors  []CallError `json:"simulatedErrors"`
	// Error is set when execution fails before the EVM runs (e.g. intrinsic gas too low).
	// It captures the pre-execution error that ApplyMessage returns.
	Error string `json:"error,omitempty"`
}

// SimulateBlockGasResult is the result of xatu_simulateBlockGas.
type SimulateBlockGasResult struct {
	BlockNumber     uint64                   `json:"blockNumber"`
	Original        BlockGasSummary          `json:"original"`
	Simulated       BlockGasSummary          `json:"simulated"`
	Transactions    []TxSummary              `json:"transactions"`
	OpcodeBreakdown map[string]OpcodeSummary `json:"opcodeBreakdown"`
}

// SimulateTransactionGasRequest is the request for xatu_simulateTransactionGas.
type SimulateTransactionGasRequest struct {
	TransactionHash string             `json:"transactionHash"`
	BlockNumber     uint64             `json:"blockNumber"`
	GasSchedule     *CustomGasSchedule `json:"gasSchedule"`
	MaxGasLimit     bool               `json:"maxGasLimit"`
}

// TxGasDetail provides detailed gas breakdown for a transaction.
type TxGasDetail struct {
	GasUsed      uint64 `json:"gasUsed"`
	IntrinsicGas uint64 `json:"intrinsicGas"`
	ExecutionGas uint64 `json:"executionGas"`
}

// SimulateTransactionGasResult is the result of xatu_simulateTransactionGas.
type SimulateTransactionGasResult struct {
	TransactionHash string                   `json:"transactionHash"`
	BlockNumber     uint64                   `json:"blockNumber"`
	Status          string                   `json:"status"`
	Original        TxGasDetail              `json:"original"`
	Simulated       TxGasDetail              `json:"simulated"`
	OpcodeBreakdown map[string]OpcodeSummary `json:"opcodeBreakdown"`
}

// executionResult holds the result of a single EVM execution.
type executionResult struct {
	GasUsed      uint64
	IntrinsicGas uint64
	Err          error // EVM execution error (from ExecResult.Err)
	ApplyErr     error // Pre-execution error (from ApplyMessage return, e.g. intrinsic gas too low)
	Status       string
	RevertCount  uint64      // Number of REVERT opcodes executed (includes nested calls)
	OpcodeCount  uint64      // Total number of opcodes executed
	CallErrors   []CallError // Errors from nested calls
}

// SimulateBlockGas re-executes a block with a custom gas schedule.
// It runs two parallel EVM executions per transaction: one with standard gas costs
// and one with the custom gas schedule. This ensures accurate gas accounting.
func (s *Service) SimulateBlockGas(
	ctx context.Context,
	req SimulateBlockGasRequest,
) (*SimulateBlockGasResult, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get block
	block, err := s.blockReader.BlockByNumber(ctx, tx, req.BlockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", req.BlockNumber, err)
	}

	if block == nil {
		return nil, fmt.Errorf("block %d not found", req.BlockNumber)
	}

	header := block.Header()
	txNumReader := s.blockReader.TxnumReader()

	// Initialize result
	result := &SimulateBlockGasResult{
		BlockNumber: req.BlockNumber,
		Original: BlockGasSummary{
			GasLimit: header.GasLimit,
		},
		Simulated: BlockGasSummary{
			GasLimit: header.GasLimit,
		},
		Transactions:    make([]TxSummary, 0, len(block.Transactions())),
		OpcodeBreakdown: make(map[string]OpcodeSummary, 64),
	}

	// Execute each transaction with dual parallel execution
	for txIndex, txn := range block.Transactions() {
		// Run both executions in parallel
		dualResult, err := s.executeTransactionDual(
			ctx, tx, header, block, txIndex, txNumReader, req.GasSchedule, req.MaxGasLimit,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to execute tx %d: %w", txIndex, err)
		}

		// GasUsed from ApplyMessage already includes intrinsic gas
		originalGas := dualResult.Original.GasUsed
		simulatedGas := dualResult.Simulated.GasUsed

		// Calculate delta percent
		var deltaPercent float64
		if originalGas > 0 {
			deltaPercent = (float64(simulatedGas) - float64(originalGas)) / float64(originalGas) * 100
		}

		// Determine if execution paths diverged
		// Divergence occurs when opcode counts differ OR status changed between original and simulated
		diverged := dualResult.Original.OpcodeCount != dualResult.Simulated.OpcodeCount ||
			dualResult.Original.Status != dualResult.Simulated.Status

		// Surface pre-execution errors (e.g. "intrinsic gas too low") from either execution
		var txError string
		if dualResult.Original.ApplyErr != nil {
			txError = "original: " + dualResult.Original.ApplyErr.Error()
		} else if dualResult.Simulated.ApplyErr != nil {
			txError = dualResult.Simulated.ApplyErr.Error()
		}

		// Add transaction summary
		txSummary := TxSummary{
			Hash:             txn.Hash().Hex(),
			Index:            uint64(txIndex),
			OriginalStatus:   dualResult.Original.Status,
			SimulatedStatus:  dualResult.Simulated.Status,
			OriginalGas:      originalGas,
			SimulatedGas:     simulatedGas,
			DeltaPercent:     deltaPercent,
			Diverged:         diverged,
			OriginalReverts:  dualResult.Original.RevertCount,
			SimulatedReverts: dualResult.Simulated.RevertCount,
			OriginalErrors:   dualResult.Original.CallErrors,
			SimulatedErrors:  dualResult.Simulated.CallErrors,
			Error:            txError,
		}
		result.Transactions = append(result.Transactions, txSummary)

		// Accumulate totals
		result.Original.GasUsed += originalGas
		result.Simulated.GasUsed += simulatedGas

		// Aggregate opcode breakdown from both executions
		for opcode, summary := range dualResult.OpcodeBreakdown {
			existing := result.OpcodeBreakdown[opcode]
			existing.OriginalCount += summary.OriginalCount
			existing.OriginalGas += summary.OriginalGas
			existing.SimulatedCount += summary.SimulatedCount
			existing.SimulatedGas += summary.SimulatedGas
			result.OpcodeBreakdown[opcode] = existing
		}

		// Add intrinsic gas to opcode breakdown so it's visible in the Gas Breakdown tab
		intrinsic := result.OpcodeBreakdown["TX_INTRINSIC"]
		intrinsic.OriginalCount++
		intrinsic.OriginalGas += dualResult.Original.IntrinsicGas
		intrinsic.SimulatedCount++
		intrinsic.SimulatedGas += dualResult.Simulated.IntrinsicGas
		result.OpcodeBreakdown["TX_INTRINSIC"] = intrinsic
	}

	// Check if gas would exceed limit
	result.Original.WouldExceedLimit = result.Original.GasUsed > header.GasLimit
	result.Simulated.WouldExceedLimit = result.Simulated.GasUsed > header.GasLimit

	return result, nil
}

// SimulateTransactionGas re-executes a single transaction with a custom gas schedule.
func (s *Service) SimulateTransactionGas(
	ctx context.Context,
	req SimulateTransactionGasRequest,
) (*SimulateTransactionGasResult, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	txHash := common.HexToHash(req.TransactionHash)

	// Look up transaction
	blockNum, txNum, ok, err := s.blockReader.TxnLookup(ctx, tx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to lookup transaction: %w", err)
	}

	if !ok {
		return nil, fmt.Errorf("transaction %s not found", req.TransactionHash)
	}

	// Verify block number matches if provided
	if req.BlockNumber != 0 && req.BlockNumber != blockNum {
		return nil, fmt.Errorf("transaction %s is in block %d, not %d", req.TransactionHash, blockNum, req.BlockNumber)
	}

	txNumReader := s.blockReader.TxnumReader()

	// Calculate txIndex
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

	// Run both executions in parallel
	dualResult, err := s.executeTransactionDual(
		ctx, tx, header, block, txIndex, txNumReader, req.GasSchedule, req.MaxGasLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to execute transaction: %w", err)
	}

	// Build result
	// Note: GasUsed from ApplyMessage already includes intrinsic gas
	// Intrinsic gas is calculated by Erigon and returned in the execution result
	// Safely calculate execution gas (avoid uint64 underflow when GasUsed < IntrinsicGas,
	// which happens when a tx fails pre-execution e.g. intrinsic gas too low)
	originalExecGas := uint64(0)
	if dualResult.Original.GasUsed > dualResult.Original.IntrinsicGas {
		originalExecGas = dualResult.Original.GasUsed - dualResult.Original.IntrinsicGas
	}

	simulatedExecGas := uint64(0)
	if dualResult.Simulated.GasUsed > dualResult.Simulated.IntrinsicGas {
		simulatedExecGas = dualResult.Simulated.GasUsed - dualResult.Simulated.IntrinsicGas
	}

	result := &SimulateTransactionGasResult{
		TransactionHash: req.TransactionHash,
		BlockNumber:     blockNum,
		Status:          dualResult.Original.Status,
		Original: TxGasDetail{
			GasUsed:      dualResult.Original.GasUsed,
			IntrinsicGas: dualResult.Original.IntrinsicGas,
			ExecutionGas: originalExecGas,
		},
		Simulated: TxGasDetail{
			GasUsed:      dualResult.Simulated.GasUsed,
			IntrinsicGas: dualResult.Simulated.IntrinsicGas,
			ExecutionGas: simulatedExecGas,
		},
		OpcodeBreakdown: dualResult.OpcodeBreakdown,
	}

	return result, nil
}

// dualExecutionResult holds the combined results from both EVM executions.
type dualExecutionResult struct {
	Original        *executionResult
	Simulated       *executionResult
	OpcodeBreakdown map[string]OpcodeSummary
}

// executeTransactionDual runs two EVM executions for a transaction:
// one with standard gas costs (original) and one with custom gas schedule (simulated).
// Both executions have tracers attached to capture per-opcode gas breakdown.
func (s *Service) executeTransactionDual(
	ctx context.Context,
	_ kv.TemporalTx, // unused - we open fresh transactions for each execution
	header *erigontypes.Header,
	block *erigontypes.Block,
	txIndex int,
	txNumReader rawdbv3.TxNumsReader,
	gasSchedule *CustomGasSchedule,
	maxGasLimit bool,
) (*dualExecutionResult, error) {
	// Execute with standard JumpTable (original gas costs)
	dbTx1, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction for original: %w", err)
	}
	defer dbTx1.Rollback()

	originalTracer := NewSimulationTracer(nil)
	originalResult, err := s.executeSingleTransaction(ctx, dbTx1, header, block, txIndex, txNumReader, nil, originalTracer, false)
	if err != nil {
		return nil, fmt.Errorf("original execution failed: %w", err)
	}

	// Capture tracer stats for original execution
	originalResult.RevertCount = originalTracer.GetRevertCount()
	originalResult.OpcodeCount = originalTracer.GetTotalOpcodeCount()
	originalResult.CallErrors = originalTracer.GetCallErrors()

	// Execute with custom JumpTable (simulated gas costs)
	dbTx2, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction for simulated: %w", err)
	}
	defer dbTx2.Rollback()

	simulatedTracer := NewSimulationTracer(gasSchedule)
	simulatedResult, err := s.executeSingleTransaction(ctx, dbTx2, header, block, txIndex, txNumReader, gasSchedule, simulatedTracer, maxGasLimit)
	if err != nil {
		return nil, fmt.Errorf("simulated execution failed: %w", err)
	}

	// Capture tracer stats for simulated execution
	simulatedResult.RevertCount = simulatedTracer.GetRevertCount()
	simulatedResult.OpcodeCount = simulatedTracer.GetTotalOpcodeCount()
	simulatedResult.CallErrors = simulatedTracer.GetCallErrors()

	// Combine opcode breakdowns from both tracers
	opcodeBreakdown := combineOpcodeBreakdowns(originalTracer, simulatedTracer)

	return &dualExecutionResult{
		Original:        originalResult,
		Simulated:       simulatedResult,
		OpcodeBreakdown: opcodeBreakdown,
	}, nil
}

// combineOpcodeBreakdowns merges the per-opcode gas data from both tracers.
// Counts and gas are tracked separately for original and simulated because
// execution paths may diverge when gas costs change.
func combineOpcodeBreakdowns(originalTracer, simulatedTracer *SimulationTracer) map[string]OpcodeSummary {
	result := make(map[string]OpcodeSummary, 64)

	// Get raw breakdowns from both tracers
	originalBreakdown := originalTracer.GetRawBreakdown()
	simulatedBreakdown := simulatedTracer.GetRawBreakdown()

	// Merge original data
	for opcode, data := range originalBreakdown {
		entry := result[opcode]
		entry.OriginalCount = data.Count
		entry.OriginalGas = data.Gas
		result[opcode] = entry
	}

	// Merge simulated data
	for opcode, data := range simulatedBreakdown {
		entry := result[opcode]
		entry.SimulatedCount = data.Count
		entry.SimulatedGas = data.Gas
		result[opcode] = entry
	}

	return result
}

// executeSingleTransaction executes a transaction with the given gas schedule.
// If gasSchedule is nil, uses the standard gas costs.
// Returns the execution result with gas used.
func (s *Service) executeSingleTransaction(
	ctx context.Context,
	dbTx kv.TemporalTx,
	header *erigontypes.Header,
	block *erigontypes.Block,
	txIndex int,
	txNumReader rawdbv3.TxNumsReader,
	gasSchedule *CustomGasSchedule,
	tracer *SimulationTracer,
	maxGasLimit bool,
) (*executionResult, error) {
	// Use chain config from DB to match what the RPC handler sees.
	execChainConfig := s.chainConfigForExecution(ctx)

	// Compute block context (creates fresh in-memory state)
	statedb, blockCtx, _, chainRules, signer, err := transactions.ComputeBlockContext(
		ctx, s.engine, header, execChainConfig, s.blockReader, nil, txNumReader, dbTx, txIndex,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to compute block context: %w", err)
	}

	// Compute tx context
	msg, txCtx, err := transactions.ComputeTxContext(statedb, s.engine, chainRules, signer, block, execChainConfig, txIndex)
	if err != nil {
		return nil, fmt.Errorf("failed to compute tx context: %w", err)
	}

	// Build VM config
	vmConfig := vm.Config{
		NoBaseFee: true,
	}

	// Set tracer if provided
	if tracer != nil {
		tracer.precompiles = vm.Precompiles(chainRules)
		statedb.SetHooks(tracer.Hooks())
		vmConfig.Tracer = tracer.Hooks()
	}

	// Build custom JumpTable if gas schedule has overrides
	if gasSchedule != nil && gasSchedule.HasOverrides() {
		customJT := BuildCustomJumpTable(chainRules, gasSchedule)
		vmConfig.CustomJumpTable = customJT
	}

	// Create EVM
	evm := vm.NewEVM(blockCtx, txCtx, statedb, execChainConfig, vmConfig)

	// Set GasSchedule for dynamic gas overrides (patched gas functions read from this)
	if gasSchedule != nil && gasSchedule.HasOverrides() {
		evm.GasSchedule = gasSchedule.ToVMGasSchedule()
	}

	// When maxGasLimit is enabled, override the transaction's gas limit with the block's
	// gas limit. This removes the gas limit as a constraining factor so the simulation
	// shows the true gas cost under the new pricing, without artificial OOG failures.
	if maxGasLimit {
		if typedMsg, ok := msg.(*erigontypes.Message); ok {
			typedMsg.ChangeGas(0, header.GasLimit)
			// Disable gas validation (EIP-7825 cap check) since this is a simulation.
			typedMsg.SetCheckGas(false)
		}
	}

	// When maxGasLimit is enabled, also enable gasBailout to skip the sender balance
	// check — the sender's balance was sufficient for the original gas limit, not the
	// overridden one.
	gasBailout := maxGasLimit
	gp := new(protocol.GasPool).AddGas(msg.Gas()).AddBlobGas(msg.BlobGas())
	execResult, err := protocol.ApplyMessage(evm, msg, gp, true, gasBailout, s.engine)

	// Determine status
	status := "success"
	if err != nil || (execResult != nil && execResult.Err != nil) {
		status = "failed"
	}

	// Calculate intrinsic gas using Erigon's function
	txn := block.Transactions()[txIndex]
	accessList := txn.GetAccessList()
	var accessListLen, storageKeysLen uint64
	if accessList != nil {
		accessListLen = uint64(len(accessList))
		storageKeysLen = uint64(accessList.StorageKeys())
	}
	intrinsicGas, _, _ := fixedgas.IntrinsicGas(
		txn.GetData(),
		accessListLen,
		storageKeysLen,
		txn.GetTo() == nil,
		chainRules.IsHomestead,
		chainRules.IsIstanbul,
		chainRules.IsShanghai,
		chainRules.IsPrague,
		false, // isAATxn
		0,     // authorizationsLen
	)
	if gasSchedule != nil {
		vmSchedule := gasSchedule.ToVMGasSchedule()
		if vmSchedule != nil && vmSchedule.HasIntrinsicOverrides() {
			intrinsicGas, _ = vm.CalcCustomIntrinsicGas(
				vmSchedule, txn.GetData(), accessListLen, storageKeysLen,
				txn.GetTo() == nil, chainRules.IsHomestead, chainRules.IsIstanbul,
				chainRules.IsShanghai, chainRules.IsPrague, false, 0,
			)
		}
	}

	result := &executionResult{
		Status:       status,
		IntrinsicGas: intrinsicGas,
		ApplyErr:     err, // Captures pre-execution errors (e.g. intrinsic gas too low)
	}

	if execResult != nil {
		result.GasUsed = execResult.ReceiptGasUsed
		result.Err = execResult.Err
	}

	return result, nil
}

// GetGasSchedule returns the gas schedule for a specific block's fork.
// Only parameters valid for that fork are included.
// Returns values and descriptions for each gas parameter.
func (s *Service) GetGasSchedule(ctx context.Context, blockNumber uint64) (*GasScheduleResponse, error) {
	tx, err := s.db.BeginTemporalRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get block header to determine fork rules
	block, err := s.blockReader.BlockByNumber(ctx, tx, blockNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", blockNumber, err)
	}

	if block == nil {
		return nil, fmt.Errorf("block %d not found", blockNumber)
	}

	header := block.Header()
	txNumReader := s.blockReader.TxnumReader()

	// Get chain rules for this block (use DB chain config for correct fork rules)
	execChainConfig := s.chainConfigForExecution(ctx)

	_, blockCtx, _, chainRules, _, err := transactions.ComputeBlockContext(
		ctx, s.engine, header, execChainConfig, s.blockReader, nil, txNumReader, tx, 0,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to compute block context: %w", err)
	}
	_ = blockCtx // Not needed, just used to get chainRules

	return GasScheduleResponseForRules(chainRules), nil
}
