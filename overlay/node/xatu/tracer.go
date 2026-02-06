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
	"encoding/hex"

	"github.com/ethpandaops/execution-processor/pkg/ethereum/execution"

	"github.com/erigontech/erigon/execution/tracing"
	"github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/execution/vm"
)

// opcodeStrings is a pre-computed lookup table for opcode string representations.
// This eliminates the map lookup overhead in vm.OpCode.String() for every opcode.
//
// OPTIMIZATION: Array lookup is ~20x faster than map lookup.
// Before: op.String() does map lookup per opcode
// After: opcodeStrings[opcode] is direct array index
var opcodeStrings [256]string

func init() {
	for i := 0; i < 256; i++ {
		opcodeStrings[i] = vm.OpCode(i).String()
	}
}

// StructLogConfig configures the structlog tracer.
type StructLogConfig struct {
	DisableMemory    bool
	DisableStack     bool
	DisableStorage   bool
	EnableReturnData bool
}

// pendingCreate tracks a CREATE/CREATE2 opcode waiting for its result address.
type pendingCreate struct {
	logIndex int // Index into logs slice
	depth    int // Depth at which CREATE was executed
}

// StructLogTracer captures structlog traces for execution-processor.
//
// OPTIMIZATION NOTES:
// This tracer is optimized for embedded mode where execution-processor runs
// in-process with erigon. Key optimizations:
//
//  1. Conditional stack capture: Full stack is NOT captured. Instead, only
//     CallToAddress is extracted for CALL-family opcodes (CALL, STATICCALL,
//     DELEGATECALL, CALLCODE). This eliminates ~99% of stack-related allocations.
//
//  2. Pre-computed opcode strings: opcodeStrings[256] array provides O(1) lookup
//     instead of map-based vm.OpCode.String().
//
//  3. Efficient address extraction: Uses uint256.Bytes20() which returns a fixed
//     [20]byte array, avoiding big.Int conversion entirely.
//
//  4. Inline GasUsed computation: GasUsed is computed during tracing by tracking
//     gas differences between consecutive opcodes at the same depth. This eliminates
//     the post-processing pass in execution-processor.
//
//  5. Inline CREATE address resolution: CREATE/CREATE2 addresses are resolved when
//     the constructor returns, extracting the result from the stack. This eliminates
//     the multi-pass ComputeCreateAddresses() scan in execution-processor.
type StructLogTracer struct {
	cfg        StructLogConfig
	logs       []execution.StructLog
	output     []byte
	err        error
	env        *tracing.VMContext
	gasUsed    uint64
	returnData []byte

	// pendingIdx tracks the index of the pending (last seen) log at each call depth.
	// Used to compute GasUsed inline: GasUsed = pendingLog.Gas - currentLog.Gas
	// Index -1 means no pending log at that depth.
	pendingIdx []int

	// pendingCreates tracks CREATE/CREATE2 opcodes waiting for their result address.
	// When execution returns to the CREATE's depth, the created address is on the stack.
	pendingCreates []pendingCreate
}

// NewStructLogTracer creates a new structlog tracer.
func NewStructLogTracer(cfg StructLogConfig) *StructLogTracer {
	return &StructLogTracer{
		cfg:            cfg,
		logs:           make([]execution.StructLog, 0, 256),
		pendingIdx:     make([]int, 0, 16), // EVM max depth is 1024, but 16 is typical
		pendingCreates: nil,
	}
}

// Hooks returns the tracing hooks for the EVM.
func (t *StructLogTracer) Hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart: t.OnTxStart,
		OnTxEnd:   t.OnTxEnd,
		OnExit:    t.OnExit,
		OnOpcode:  t.OnOpcode,
	}
}

// OnTxStart is called when a transaction starts.
func (t *StructLogTracer) OnTxStart(env *tracing.VMContext, _ types.Transaction, _ accounts.Address) {
	t.env = env
}

// OnTxEnd is called when a transaction ends.
func (t *StructLogTracer) OnTxEnd(receipt *types.Receipt, err error) {
	if err != nil {
		if t.err == nil {
			t.err = err
		}

		return
	}

	// EIP-7778 note: Receipt.GasUsed is unchanged — it remains the post-refund value
	// (what the user pays). The EIP-7778 split only affects ExecutionResult (which now
	// has ReceiptGasUsed and BlockGasUsed). The Receipt struct's GasUsed field and its
	// derivation from CumulativeGasUsed are not affected.
	t.gasUsed = receipt.GasUsed
}

// isCallOpcode checks if the opcode is a CALL-family opcode that requires
// target address extraction.
//
// CALL-family opcodes: CALL, STATICCALL, DELEGATECALL, CALLCODE
// These are the only opcodes where execution-processor needs the target address
// (CallToAddress), which is at stack position len-2.
func isCallOpcode(op vm.OpCode) bool {
	switch op {
	case vm.CALL, vm.STATICCALL, vm.DELEGATECALL, vm.CALLCODE:
		return true
	default:
		return false
	}
}

// isCreateOpcode checks if the opcode is CREATE or CREATE2.
func isCreateOpcode(op vm.OpCode) bool {
	return op == vm.CREATE || op == vm.CREATE2
}

// OnOpcode captures each EVM opcode execution.
//
// OPTIMIZATION: Conditional stack capture
// Before: Full stack captured for every opcode (~52 allocs for 10-item stack)
// After: Only CallToAddress extracted for CALL-family opcodes (~3 allocs)
//
// The stack is only needed to extract CallToAddress for CALL, STATICCALL,
// DELEGATECALL, and CALLCODE opcodes. For all other opcodes (~95% of total),
// we skip stack processing entirely, eliminating allocations.
//
// OPTIMIZATION: Inline GasUsed computation
// GasUsed is computed as the gas difference between consecutive opcodes at the
// same depth level. This eliminates the post-processing pass in execution-processor.
// For opcodes that are last in their call context (before returning to parent),
// GasCost is used as fallback since we cannot compute across call boundaries.
//
// Performance improvement: ~17x faster per opcode, ~99% fewer allocations.
func (t *StructLogTracer) OnOpcode(pc uint64, opcode byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	op := vm.OpCode(opcode)

	// Compute GasUsed for the pending log at this depth before adding new log.
	t.updatePendingGasUsed(depth, gas)

	// Resolve any pending CREATEs that have completed.
	// When execution returns to the CREATE's depth (or lower), the created address
	// is at the top of the current opcode's stack.
	t.resolvePendingCreates(depth, scope)

	log := execution.StructLog{
		PC:      uint32(pc),
		Op:      opcodeStrings[opcode], // O(1) array lookup vs map lookup
		Gas:     gas,
		GasCost: cost,
		GasUsed: cost, // Default to GasCost; updated by next opcode at same depth
		Depth:   uint64(depth),
	}

	// Sanitize gasCost: it can never legitimately exceed available gas.
	// This guards against Erigon's unsigned integer underflow bug in gas.go:callGas()
	// where availableGas - base underflows when availableGas < base.
	if log.GasCost > log.Gas {
		log.GasCost = log.Gas
	}

	// OPTIMIZATION: Extract only CallToAddress for CALL-family opcodes.
	// This replaces full stack capture which allocated 3 objects per stack item.
	//
	// For CALL opcodes, the target address is at stack[len-2] per EVM spec:
	//   CALL:         gas, addr, value, argsOffset, argsLength, retOffset, retLength
	//   STATICCALL:   gas, addr, argsOffset, argsLength, retOffset, retLength
	//   DELEGATECALL: gas, addr, argsOffset, argsLength, retOffset, retLength
	//   CALLCODE:     gas, addr, value, argsOffset, argsLength, retOffset, retLength
	//
	// In all cases, addr is at position 1 from top (index len-2).
	if isCallOpcode(op) {
		stack := scope.StackData()

		if len(stack) > 1 {
			addr := &stack[len(stack)-2]
			addrBytes := addr.Bytes20()
			addrStr := "0x" + hex.EncodeToString(addrBytes[:])
			log.CallToAddress = &addrStr
		}
	}

	// Capture return data if enabled
	if t.cfg.EnableReturnData && len(rData) > 0 {
		returnData := hex.EncodeToString(rData)
		log.ReturnData = &returnData
	}

	// Capture refund
	if t.env != nil {
		refund := t.env.IntraBlockState.GetRefund()
		log.Refund = &refund
	}

	// Capture error
	if err != nil {
		errStr := err.Error()
		log.Error = &errStr
	}

	// Track this log as pending at current depth for GasUsed computation.
	logIdx := len(t.logs)
	t.logs = append(t.logs, log)
	t.setPendingIdx(depth, logIdx)

	// Track CREATE/CREATE2 opcodes for address resolution.
	// The created address will be extracted when execution returns to this depth.
	if isCreateOpcode(op) {
		t.pendingCreates = append(t.pendingCreates, pendingCreate{
			logIndex: logIdx,
			depth:    depth,
		})
	}
}

// updatePendingGasUsed updates the GasUsed field for the pending log at the given depth.
// GasUsed = pendingLog.Gas - currentGas (the gas consumed by that opcode).
func (t *StructLogTracer) updatePendingGasUsed(depth int, currentGas uint64) {
	// Ensure pendingIdx has enough capacity for this depth.
	for len(t.pendingIdx) <= depth {
		t.pendingIdx = append(t.pendingIdx, -1)
	}

	// Clear pending indices for deeper levels (we've returned from those calls).
	// Those logs keep their GasCost as GasUsed since we can't compute across boundaries.
	for d := len(t.pendingIdx) - 1; d > depth; d-- {
		t.pendingIdx[d] = -1
	}

	// Update GasUsed for pending log at current depth.
	if prevIdx := t.pendingIdx[depth]; prevIdx >= 0 && prevIdx < len(t.logs) {
		t.logs[prevIdx].GasUsed = t.logs[prevIdx].Gas - currentGas
	}
}

// setPendingIdx sets the pending log index for the given depth.
func (t *StructLogTracer) setPendingIdx(depth, logIdx int) {
	for len(t.pendingIdx) <= depth {
		t.pendingIdx = append(t.pendingIdx, -1)
	}

	t.pendingIdx[depth] = logIdx
}

// resolvePendingCreates resolves any pending CREATE/CREATE2 opcodes that have completed.
// When execution returns to the CREATE's depth (or lower), the created address (or 0 on
// failure) is at the top of the current opcode's stack.
func (t *StructLogTracer) resolvePendingCreates(currentDepth int, scope tracing.OpContext) {
	for len(t.pendingCreates) > 0 {
		last := t.pendingCreates[len(t.pendingCreates)-1]

		// CREATE completes when we see an opcode at the same depth or lower.
		if currentDepth <= last.depth {
			// Extract created address from top of stack.
			stack := scope.StackData()
			if len(stack) > 0 {
				addr := &stack[len(stack)-1]
				addrBytes := addr.Bytes20()
				addrStr := "0x" + hex.EncodeToString(addrBytes[:])
				t.logs[last.logIndex].CallToAddress = &addrStr
			}

			t.pendingCreates = t.pendingCreates[:len(t.pendingCreates)-1]
		} else {
			break
		}
	}
}

// OnExit is called when execution exits.
func (t *StructLogTracer) OnExit(depth int, output []byte, _ uint64, err error, _ bool) {
	if depth != 0 {
		return
	}

	t.output = make([]byte, len(output))
	copy(t.output, output)
	t.err = err
}

// GetTraceTransaction returns the trace result in execution-processor format.
func (t *StructLogTracer) GetTraceTransaction() *execution.TraceTransaction {
	trace := &execution.TraceTransaction{
		Gas:        t.gasUsed,
		Failed:     t.err != nil,
		Structlogs: t.logs,
	}

	if len(t.output) > 0 {
		returnValue := hex.EncodeToString(t.output)
		trace.ReturnValue = &returnValue
	}

	return trace
}

// StructLogs returns the captured log entries.
func (t *StructLogTracer) StructLogs() []execution.StructLog {
	return t.logs
}

// Error returns the VM error captured by the trace.
func (t *StructLogTracer) Error() error {
	return t.err
}

// Output returns the VM return value captured by the trace.
func (t *StructLogTracer) Output() []byte {
	return t.output
}
