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
	"github.com/erigontech/erigon/execution/tracing"
	"github.com/erigontech/erigon/execution/types"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/holiman/uint256"
)

// OpcodeSummary tracks gas usage for a single opcode type.
// Counts and gas are tracked separately for original and simulated executions
// because execution paths may diverge (e.g., out-of-gas earlier with higher costs).
type OpcodeSummary struct {
	OriginalCount  uint64 `json:"originalCount"`
	OriginalGas    uint64 `json:"originalGas"`
	SimulatedCount uint64 `json:"simulatedCount"`
	SimulatedGas   uint64 `json:"simulatedGas"`
}

// CallError represents an error that occurred during a nested call.
type CallError struct {
	Depth   int    `json:"depth"`
	Type    string `json:"type"`    // "CALL", "DELEGATECALL", "STATICCALL", "CREATE", etc.
	Error   string `json:"error"`   // "execution reverted", "out of gas", etc.
	Address string `json:"address"` // Target contract address (truncated)
}

// callFrame tracks the current call being executed.
type callFrame struct {
	depth   int
	typ     string
	address string
}

// SimulationTracer tracks opcode execution during gas simulation.
// It observes the gas costs charged by the EVM (which may be using a custom JumpTable)
// and records per-opcode statistics.
type SimulationTracer struct {
	schedule *CustomGasSchedule

	// Per-opcode tracking
	gasUsed      map[string]uint64 // opcode -> total gas used
	opcodeCounts map[string]uint64 // opcode -> count

	// Total tracking
	totalGasUsed uint64

	// Call error tracking
	callStack  []callFrame // Stack of active calls
	callErrors []CallError // Errors that occurred during execution

	// Pending CALL tracking - for accurate gas attribution
	// CALL-family opcodes report cost = overhead + childGas in OnOpcode,
	// but we only want to track overhead. OnEnter tells us childGas.
	pendingCallCost  uint64 // Cost from OnOpcode, resolved in OnEnter
	pendingCallDepth int    // Depth where the CALL was made
	pendingCallType  string // Opcode name (CALL, STATICCALL, etc.)

	// VM context
	env *tracing.VMContext
}

// NewSimulationTracer creates a new simulation tracer.
func NewSimulationTracer(schedule *CustomGasSchedule) *SimulationTracer {
	return &SimulationTracer{
		schedule:     schedule,
		gasUsed:      make(map[string]uint64, 64),
		opcodeCounts: make(map[string]uint64, 64),
		callStack:    make([]callFrame, 0, 16),
		callErrors:   make([]CallError, 0, 8),
	}
}

// Hooks returns the tracing hooks for the EVM.
func (t *SimulationTracer) Hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart: t.OnTxStart,
		OnTxEnd:   t.OnTxEnd,
		OnEnter:   t.OnEnter,
		OnExit:    t.OnExit,
		OnOpcode:  t.OnOpcode,
	}
}

// OnTxStart is called when a transaction starts.
func (t *SimulationTracer) OnTxStart(env *tracing.VMContext, txn types.Transaction, from accounts.Address) {
	t.env = env
	t.totalGasUsed = 0
}

// OnTxEnd is called when a transaction ends.
func (t *SimulationTracer) OnTxEnd(_ *types.Receipt, _ error) {
	// Flush any unresolved pending CALL (edge case: tx ends abnormally after CALL)
	if t.pendingCallCost > 0 {
		t.gasUsed[t.pendingCallType] += t.pendingCallCost
		t.totalGasUsed += t.pendingCallCost
		t.pendingCallCost = 0
		t.pendingCallDepth = 0
		t.pendingCallType = ""
	}
}

// OnEnter is called when a call frame is entered.
func (t *SimulationTracer) OnEnter(depth int, typ byte, from accounts.Address, to accounts.Address, precompile bool, input []byte, gas uint64, value uint256.Int, code []byte) {
	// Get the call type name from the opcode
	typName := opcodeStrings[typ]
	if typName == "" {
		typName = "UNKNOWN"
	}

	// Resolve pending CALL gas - compute overhead by subtracting child allocation
	// OnEnter depth is the SAME as parent's depth (evm.depth before Run() increments it)
	if t.pendingCallCost > 0 && t.pendingCallDepth == depth {
		// overhead = total cost charged - gas allocated to child
		var overhead uint64
		if t.pendingCallCost > gas {
			overhead = t.pendingCallCost - gas
		}
		// Attribute overhead to the CALL opcode
		t.gasUsed[t.pendingCallType] += overhead
		t.totalGasUsed += overhead
		// Clear pending
		t.pendingCallCost = 0
		t.pendingCallDepth = 0
		t.pendingCallType = ""
	}

	// Truncate address to first 20 chars (0x + 18 hex chars)
	addrStr := to.String()
	if len(addrStr) > 20 {
		addrStr = addrStr[:20]
	}

	// Push call frame onto stack
	t.callStack = append(t.callStack, callFrame{
		depth:   depth,
		typ:     typName,
		address: addrStr,
	})
}

// OnExit is called when a call frame exits.
func (t *SimulationTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	// Pop from call stack
	if len(t.callStack) == 0 {
		return
	}

	frame := t.callStack[len(t.callStack)-1]
	t.callStack = t.callStack[:len(t.callStack)-1]

	// Record error if call failed
	if err != nil || reverted {
		errMsg := "execution reverted"
		if err != nil {
			errMsg = err.Error()
		}

		t.callErrors = append(t.callErrors, CallError{
			Depth:   frame.depth,
			Type:    frame.typ,
			Error:   errMsg,
			Address: frame.address,
		})
	}
}

// OnOpcode captures each EVM opcode execution and records the gas cost.
// The cost parameter is the actual gas charged by the EVM, which will reflect
// the custom JumpTable gas costs if one is being used.
//
// For CALL-family opcodes (CALL, STATICCALL, DELEGATECALL, CALLCODE), the cost
// includes gas allocated to the child frame. We defer gas tracking to OnEnter
// where we can compute: overhead = cost - childGas.
func (t *SimulationTracer) OnOpcode(pc uint64, opcode byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	opName := opcodeStrings[opcode]

	// Check if there's an unresolved pending CALL at the same depth
	// This happens when a CALL fails before OnEnter (e.g., insufficient balance)
	if t.pendingCallCost > 0 && t.pendingCallDepth == depth {
		// Previous CALL failed without creating child frame - attribute full cost
		t.gasUsed[t.pendingCallType] += t.pendingCallCost
		t.totalGasUsed += t.pendingCallCost
		t.pendingCallCost = 0
		t.pendingCallDepth = 0
		t.pendingCallType = ""
	}

	// Always track opcode counts
	t.opcodeCounts[opName]++

	// For CALL-family opcodes, defer gas tracking to OnEnter
	// Opcodes: CALL=0xF1, CALLCODE=0xF2, DELEGATECALL=0xF4, STATICCALL=0xFA
	if opcode == 0xF1 || opcode == 0xF2 || opcode == 0xF4 || opcode == 0xFA {
		t.pendingCallCost = cost
		t.pendingCallDepth = depth
		t.pendingCallType = opName
		return
	}

	t.gasUsed[opName] += cost
	t.totalGasUsed += cost
}

// TracerBreakdown is the raw data from a single tracer execution.
type TracerBreakdown struct {
	Count uint64
	Gas   uint64
}

// GetRawBreakdown returns the raw per-opcode data from this tracer's execution.
// The caller combines data from two tracers (original and simulated) into OpcodeSummary.
func (t *SimulationTracer) GetRawBreakdown() map[string]TracerBreakdown {
	result := make(map[string]TracerBreakdown, len(t.opcodeCounts))

	for opcode, count := range t.opcodeCounts {
		gas := t.gasUsed[opcode]
		result[opcode] = TracerBreakdown{
			Count: count,
			Gas:   gas,
		}
	}

	return result
}

// GetTotalGasUsed returns the total gas used by all opcodes.
func (t *SimulationTracer) GetTotalGasUsed() uint64 {
	return t.totalGasUsed
}

// GetRevertCount returns the number of REVERT opcodes executed.
// This includes reverts from nested calls, not just the top-level transaction.
func (t *SimulationTracer) GetRevertCount() uint64 {
	return t.opcodeCounts["REVERT"]
}

// GetTotalOpcodeCount returns the total number of opcodes executed.
func (t *SimulationTracer) GetTotalOpcodeCount() uint64 {
	var total uint64
	for _, count := range t.opcodeCounts {
		total += count
	}
	return total
}

// GetCallErrors returns all call errors that occurred during execution.
func (t *SimulationTracer) GetCallErrors() []CallError {
	return t.callErrors
}

// Reset clears the tracer state for reuse.
func (t *SimulationTracer) Reset() {
	for k := range t.gasUsed {
		delete(t.gasUsed, k)
	}
	for k := range t.opcodeCounts {
		delete(t.opcodeCounts, k)
	}
	t.totalGasUsed = 0
	t.callStack = t.callStack[:0]
	t.callErrors = t.callErrors[:0]
	t.pendingCallCost = 0
	t.pendingCallDepth = 0
	t.pendingCallType = ""
}

// Note: opcodeStrings is defined in tracer.go and shared across the package.
