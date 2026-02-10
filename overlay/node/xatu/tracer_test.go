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
	"testing"

	"github.com/holiman/uint256"

	"github.com/erigontech/erigon/execution/tracing"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/execution/vm"
)

// =============================================================================
// StructLogTracer Unit Tests
// =============================================================================

// mockIntraBlockState implements tracing.IntraBlockState for testing.
type mockIntraBlockState struct {
	refund uint64
}

func (m *mockIntraBlockState) GetBalance(accounts.Address) (uint256.Int, error) {
	return uint256.Int{}, nil
}
func (m *mockIntraBlockState) GetNonce(accounts.Address) (uint64, error) { return 0, nil }
func (m *mockIntraBlockState) GetCode(accounts.Address) ([]byte, error)  { return nil, nil }
func (m *mockIntraBlockState) GetState(accounts.Address, accounts.StorageKey) (uint256.Int, error) {
	return uint256.Int{}, nil
}
func (m *mockIntraBlockState) Exist(accounts.Address) (bool, error) { return false, nil }
func (m *mockIntraBlockState) GetRefund() uint64                    { return m.refund }

// TestRefundCapture verifies that refund values are captured when OnTxStart
// is called to initialize the tracer with a VMContext.
//
// This test ensures that the fix for the missing OnTxStart call in
// executeWithTracer() is working correctly. Without OnTxStart being called,
// tracer.env is nil and refund capture is silently skipped.
func TestRefundCapture(t *testing.T) {
	tests := []struct {
		name           string
		callOnTxStart  bool
		refundValue    uint64
		expectRefund   bool
		expectedRefund uint64
	}{
		{
			name:           "with OnTxStart - refund captured",
			callOnTxStart:  true,
			refundValue:    59700,
			expectRefund:   true,
			expectedRefund: 59700,
		},
		{
			name:           "with OnTxStart - zero refund captured",
			callOnTxStart:  true,
			refundValue:    0,
			expectRefund:   true,
			expectedRefund: 0,
		},
		{
			name:          "without OnTxStart - refund nil",
			callOnTxStart: false,
			refundValue:   59700,
			expectRefund:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracer := NewStructLogTracer(StructLogConfig{})
			ctx := newMockOpContext(10)

			// Conditionally call OnTxStart to simulate the fix
			if tc.callOnTxStart {
				mockState := &mockIntraBlockState{refund: tc.refundValue}
				vmCtx := &tracing.VMContext{
					IntraBlockState: mockState,
				}
				tracer.OnTxStart(vmCtx, nil, accounts.Address{})
			}

			// Simulate an opcode execution
			tracer.OnOpcode(
				0,            // pc
				byte(vm.ADD), // opcode
				100000,       // gas
				3,            // cost
				ctx,          // scope
				nil,          // rData
				1,            // depth
				nil,          // err
			)

			logs := tracer.StructLogs()
			if len(logs) != 1 {
				t.Fatalf("expected 1 log, got %d", len(logs))
			}

			if tc.expectRefund {
				if logs[0].Refund == nil {
					t.Errorf("expected Refund to be non-nil, got nil")
				} else if *logs[0].Refund != tc.expectedRefund {
					t.Errorf("Refund = %d, want %d", *logs[0].Refund, tc.expectedRefund)
				}
			} else {
				if logs[0].Refund != nil {
					t.Errorf("expected Refund to be nil, got %d", *logs[0].Refund)
				}
			}
		})
	}
}

// TestRefundCaptureAcrossOpcodes verifies that the maximum refund value
// is tracked correctly across multiple opcodes as the refund counter
// accumulates (simulating SSTORE operations that clear storage).
func TestRefundCaptureAcrossOpcodes(t *testing.T) {
	tracer := NewStructLogTracer(StructLogConfig{})
	ctx := newMockOpContext(10)

	mockState := &mockIntraBlockState{refund: 0}
	vmCtx := &tracing.VMContext{
		IntraBlockState: mockState,
	}
	tracer.OnTxStart(vmCtx, nil, accounts.Address{})

	// Simulate opcodes with increasing refund (as SSTORE clears storage)
	refundSequence := []uint64{0, 0, 4800, 4800, 9600, 9600}

	for i, refund := range refundSequence {
		mockState.refund = refund
		tracer.OnOpcode(
			uint64(i),
			byte(vm.SSTORE),
			100000,
			5000,
			ctx,
			nil,
			1,
			nil,
		)
	}

	logs := tracer.StructLogs()
	if len(logs) != len(refundSequence) {
		t.Fatalf("expected %d logs, got %d", len(refundSequence), len(logs))
	}

	// Verify each log has the correct refund value
	for i, expectedRefund := range refundSequence {
		if logs[i].Refund == nil {
			t.Errorf("log[%d]: expected Refund to be non-nil, got nil", i)
		} else if *logs[i].Refund != expectedRefund {
			t.Errorf("log[%d]: Refund = %d, want %d", i, *logs[i].Refund, expectedRefund)
		}
	}

	// The max refund should be the last value (9600)
	maxRefund := uint64(0)
	for _, log := range logs {
		if log.Refund != nil && *log.Refund > maxRefund {
			maxRefund = *log.Refund
		}
	}

	if maxRefund != 9600 {
		t.Errorf("max refund = %d, want 9600", maxRefund)
	}
}

// TestGasCostSanitization verifies that corrupted gasCost values from
// Erigon's unsigned integer underflow bug are sanitized.
//
// The bug: In gas.go:callGas(), `availableGas - base` underflows when
// availableGas < base, producing values close to 2^64.
//
// The fix: gasCost is capped at available gas (you can't consume more
// than you have).
func TestGasCostSanitization(t *testing.T) {
	tests := []struct {
		name            string
		gas             uint64
		cost            uint64 // gasCost from EVM
		expectedGasCost uint64
	}{
		{
			name:            "normal opcode - no change",
			gas:             10000,
			cost:            3,
			expectedGasCost: 3,
		},
		{
			name:            "CALL opcode - no change",
			gas:             319945,
			cost:            314987,
			expectedGasCost: 314987,
		},
		{
			name:            "corrupted from underflow bug",
			gas:             5058,
			cost:            18158513697557845033, // Actual corrupted value
			expectedGasCost: 5058,                 // Sanitized to available gas
		},
		{
			name:            "max uint64 corrupted",
			gas:             1000,
			cost:            ^uint64(0),
			expectedGasCost: 1000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracer := NewStructLogTracer(StructLogConfig{})
			ctx := newMockOpContext(10)

			tracer.OnOpcode(
				0,             // pc
				byte(vm.CALL), // opcode
				tc.gas,        // gas
				tc.cost,       // cost (potentially corrupted)
				ctx,           // scope
				nil,           // rData
				1,             // depth
				nil,           // err
			)

			logs := tracer.StructLogs()
			if len(logs) != 1 {
				t.Fatalf("expected 1 log, got %d", len(logs))
			}

			if logs[0].GasCost != tc.expectedGasCost {
				t.Errorf("GasCost = %d, want %d", logs[0].GasCost, tc.expectedGasCost)
			}
		})
	}
}

// TestGasUsedComputation verifies that GasUsed is correctly computed for
// sequential opcodes at the same depth.
//
// For consecutive opcodes at the same depth:
//
//	GasUsed = prevLog.Gas - currentLog.Gas
//
// This eliminates the need for post-processing in execution-processor.
func TestGasUsedComputation(t *testing.T) {
	tracer := NewStructLogTracer(StructLogConfig{})
	ctx := newMockOpContext(10)

	// Simulate 3 sequential opcodes at depth 1
	// Gas decreases: 10000 -> 9997 -> 9990 -> 9980
	//
	// Opcode 1: gas=10000, cost=3  -> GasUsed should be 10000-9997=3
	// Opcode 2: gas=9997,  cost=7  -> GasUsed should be 9997-9990=7
	// Opcode 3: gas=9990,  cost=10 -> GasUsed stays at cost=10 (no next opcode)

	tracer.OnOpcode(0, byte(vm.ADD), 10000, 3, ctx, nil, 1, nil)
	tracer.OnOpcode(1, byte(vm.MUL), 9997, 7, ctx, nil, 1, nil)
	tracer.OnOpcode(2, byte(vm.SUB), 9990, 10, ctx, nil, 1, nil)

	logs := tracer.StructLogs()
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}

	// Opcode 1: GasUsed = 10000 - 9997 = 3
	if logs[0].GasUsed != 3 {
		t.Errorf("log[0].GasUsed = %d, want 3", logs[0].GasUsed)
	}

	// Opcode 2: GasUsed = 9997 - 9990 = 7
	if logs[1].GasUsed != 7 {
		t.Errorf("log[1].GasUsed = %d, want 7", logs[1].GasUsed)
	}

	// Opcode 3: GasUsed = cost (no next opcode to compute from)
	if logs[2].GasUsed != 10 {
		t.Errorf("log[2].GasUsed = %d, want 10", logs[2].GasUsed)
	}
}

// TestGasUsedSanitization verifies that GasUsed is capped for out-of-gas
// opcodes where Erigon reports inflated theoretical costs.
//
// The bug: When an opcode fails with "out of gas", Erigon reports the
// theoretical computed cost (e.g., 3.69 trillion for memory expansion)
// rather than the actual gas consumed (which is at most the remaining gas).
//
// The fix: GasUsed is capped at available gas.
func TestGasUsedSanitization(t *testing.T) {
	tests := []struct {
		name            string
		gas             uint64
		cost            uint64 // theoretical cost from Erigon
		hasError        bool
		expectedGasUsed uint64
	}{
		{
			name:            "normal opcode - GasUsed equals cost",
			gas:             10000,
			cost:            3,
			hasError:        false,
			expectedGasUsed: 3,
		},
		{
			name:            "OOG with massive theoretical cost",
			gas:             340375,
			cost:            3688376207808, // Actual value from block 24276761
			hasError:        true,
			expectedGasUsed: 340375, // Capped to remaining gas
		},
		{
			name:            "OOG with moderate inflation",
			gas:             137304,
			cost:            18290742255, // From block 24142418
			hasError:        true,
			expectedGasUsed: 137304,
		},
		{
			name:            "cost exactly equals gas - no change",
			gas:             5000,
			cost:            5000,
			hasError:        true,
			expectedGasUsed: 5000,
		},
		{
			name:            "cost slightly exceeds gas",
			gas:             1000,
			cost:            1001,
			hasError:        true,
			expectedGasUsed: 1000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tracer := NewStructLogTracer(StructLogConfig{})
			ctx := newMockOpContext(10)

			var err error
			if tc.hasError {
				err = vm.ErrOutOfGas
			}

			tracer.OnOpcode(
				0,              // pc
				byte(vm.MLOAD), // opcode (memory ops often cause OOG)
				tc.gas,         // gas remaining
				tc.cost,        // theoretical cost (possibly inflated)
				ctx,            // scope
				nil,            // rData
				1,              // depth
				err,            // error
			)

			logs := tracer.StructLogs()
			if len(logs) != 1 {
				t.Fatalf("expected 1 log, got %d", len(logs))
			}

			if logs[0].GasUsed != tc.expectedGasUsed {
				t.Errorf("GasUsed = %d, want %d", logs[0].GasUsed, tc.expectedGasUsed)
			}

			// Also verify GasCost is capped
			if logs[0].GasCost > logs[0].Gas {
				t.Errorf("GasCost (%d) exceeds Gas (%d)", logs[0].GasCost, logs[0].Gas)
			}
		})
	}
}

// TestGasUsedAcrossDepths verifies GasUsed behavior when call depth changes.
//
// When returning from a deeper call, the pending log at that depth cannot
// have its GasUsed computed (no next opcode at same depth), so it keeps
// the capped cost value.
func TestGasUsedAcrossDepths(t *testing.T) {
	tracer := NewStructLogTracer(StructLogConfig{})
	ctx := newMockOpContext(10)

	// Simulate: depth 1 -> depth 2 -> back to depth 1
	//
	// Op1 (depth 1): gas=10000, cost=100 -> GasUsed computed when Op4 arrives
	// Op2 (depth 2): gas=9000,  cost=50  -> GasUsed computed when Op3 arrives
	// Op3 (depth 2): gas=8950,  cost=30  -> GasUsed stays at cost (returns to depth 1)
	// Op4 (depth 1): gas=8900,  cost=20  -> Updates Op1's GasUsed

	tracer.OnOpcode(0, byte(vm.CALL), 10000, 100, ctx, nil, 1, nil)
	tracer.OnOpcode(1, byte(vm.ADD), 9000, 50, ctx, nil, 2, nil)
	tracer.OnOpcode(2, byte(vm.MUL), 8950, 30, ctx, nil, 2, nil)
	tracer.OnOpcode(3, byte(vm.POP), 8900, 20, ctx, nil, 1, nil)

	logs := tracer.StructLogs()
	if len(logs) != 4 {
		t.Fatalf("expected 4 logs, got %d", len(logs))
	}

	// Op1 (depth 1): GasUsed = 10000 - 8900 = 1100
	// This is the gas consumed by the entire CALL (including subcall)
	if logs[0].GasUsed != 1100 {
		t.Errorf("log[0].GasUsed = %d, want 1100", logs[0].GasUsed)
	}

	// Op2 (depth 2): GasUsed = 9000 - 8950 = 50
	if logs[1].GasUsed != 50 {
		t.Errorf("log[1].GasUsed = %d, want 50", logs[1].GasUsed)
	}

	// Op3 (depth 2): Last at this depth, GasUsed = cost = 30
	if logs[2].GasUsed != 30 {
		t.Errorf("log[2].GasUsed = %d, want 30", logs[2].GasUsed)
	}

	// Op4 (depth 1): Last opcode, GasUsed = cost = 20
	if logs[3].GasUsed != 20 {
		t.Errorf("log[3].GasUsed = %d, want 20", logs[3].GasUsed)
	}
}

// TestGasUsedOOGAtDepth verifies that an OOG opcode at a nested depth
// has its GasUsed correctly capped.
func TestGasUsedOOGAtDepth(t *testing.T) {
	tracer := NewStructLogTracer(StructLogConfig{})
	ctx := newMockOpContext(10)

	// Simulate: depth 1 -> depth 2 (OOG) -> back to depth 1
	//
	// Op1 (depth 1): gas=10000, cost=100
	// Op2 (depth 2): gas=5000, cost=HUGE (OOG) -> GasUsed capped to 5000
	// Op3 (depth 1): gas=4900, cost=50

	tracer.OnOpcode(0, byte(vm.CALL), 10000, 100, ctx, nil, 1, nil)
	tracer.OnOpcode(1, byte(vm.MLOAD), 5000, 999999999999, ctx, nil, 2, vm.ErrOutOfGas)
	tracer.OnOpcode(2, byte(vm.POP), 4900, 50, ctx, nil, 1, nil)

	logs := tracer.StructLogs()
	if len(logs) != 3 {
		t.Fatalf("expected 3 logs, got %d", len(logs))
	}

	// Op1: GasUsed = 10000 - 4900 = 5100
	if logs[0].GasUsed != 5100 {
		t.Errorf("log[0].GasUsed = %d, want 5100", logs[0].GasUsed)
	}

	// Op2: OOG opcode - GasUsed capped at available gas
	if logs[1].GasUsed != 5000 {
		t.Errorf("log[1].GasUsed = %d, want 5000 (capped)", logs[1].GasUsed)
	}

	// Also verify GasCost is capped
	if logs[1].GasCost != 5000 {
		t.Errorf("log[1].GasCost = %d, want 5000 (capped)", logs[1].GasCost)
	}

	// Op3: Last at depth 1, GasUsed = cost
	if logs[2].GasUsed != 50 {
		t.Errorf("log[2].GasUsed = %d, want 50", logs[2].GasUsed)
	}
}

// =============================================================================
// StructLogTracer Benchmarks
// =============================================================================
//
// These benchmarks measure the actual performance of the StructLogTracer,
// specifically the OnOpcode hot path which is called for every EVM opcode.
//
// Run with: go test -tags embedded -bench=. -benchmem ./node/xatu/...
//
// Key metrics:
//   - ns/op: Time per opcode (target: minimize)
//   - B/op: Memory allocated per opcode (target: minimize)
//   - allocs/op: Number of allocations per opcode (target: minimize)

// mockOpContext implements tracing.OpContext for benchmarking.
type mockOpContext struct {
	stack  []uint256.Int
	memory []byte
	caller accounts.Address
	addr   accounts.Address
	value  uint256.Int
	input  []byte
	code   []byte
	hash   accounts.CodeHash
}

func newMockOpContext(stackSize int) *mockOpContext {
	ctx := &mockOpContext{
		stack: make([]uint256.Int, stackSize),
	}

	// Fill stack with realistic-looking data
	for i := range ctx.stack {
		ctx.stack[i].SetUint64(uint64(0x1234567890abcdef + i))
	}

	return ctx
}

func (m *mockOpContext) MemoryData() []byte          { return m.memory }
func (m *mockOpContext) StackData() []uint256.Int    { return m.stack }
func (m *mockOpContext) Caller() accounts.Address    { return m.caller }
func (m *mockOpContext) Address() accounts.Address   { return m.addr }
func (m *mockOpContext) CallValue() uint256.Int      { return m.value }
func (m *mockOpContext) CallInput() []byte           { return m.input }
func (m *mockOpContext) Code() []byte                { return m.code }
func (m *mockOpContext) CodeHash() accounts.CodeHash { return m.hash }

// =============================================================================
// OnOpcode Benchmarks - Tests the actual tracer implementation
// =============================================================================

// BenchmarkOnOpcode_NonCall benchmarks OnOpcode for non-CALL opcodes.
// These opcodes should have ZERO stack-related allocations after optimization.
func BenchmarkOnOpcode_NonCall(b *testing.B) {
	scenarios := []struct {
		name      string
		stackSize int
		opcode    byte
	}{
		{"PUSH1_Stack5", 5, byte(vm.PUSH1)},
		{"PUSH1_Stack10", 10, byte(vm.PUSH1)},
		{"PUSH1_Stack20", 20, byte(vm.PUSH1)},
		{"SLOAD_Stack10", 10, byte(vm.SLOAD)},
		{"ADD_Stack10", 10, byte(vm.ADD)},
		{"MSTORE_Stack10", 10, byte(vm.MSTORE)},
		{"JUMP_Stack10", 10, byte(vm.JUMP)},
	}

	for _, tc := range scenarios {
		b.Run(tc.name, func(b *testing.B) {
			tracer := NewStructLogTracer(StructLogConfig{
				EnableReturnData: true,
			})
			ctx := newMockOpContext(tc.stackSize)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				tracer.OnOpcode(
					uint64(i), // pc
					tc.opcode, // opcode
					100000,    // gas
					3,         // cost
					ctx,       // scope
					nil,       // rData
					1,         // depth
					nil,       // err
				)
			}
		})
	}
}

// BenchmarkOnOpcode_Call benchmarks OnOpcode for CALL-family opcodes.
// These opcodes extract CallToAddress (~3 allocations).
func BenchmarkOnOpcode_Call(b *testing.B) {
	scenarios := []struct {
		name      string
		stackSize int
		opcode    byte
	}{
		{"CALL_Stack10", 10, byte(vm.CALL)},
		{"STATICCALL_Stack10", 10, byte(vm.STATICCALL)},
		{"DELEGATECALL_Stack10", 10, byte(vm.DELEGATECALL)},
		{"CALLCODE_Stack10", 10, byte(vm.CALLCODE)},
	}

	for _, tc := range scenarios {
		b.Run(tc.name, func(b *testing.B) {
			tracer := NewStructLogTracer(StructLogConfig{
				EnableReturnData: true,
			})
			ctx := newMockOpContext(tc.stackSize)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				tracer.OnOpcode(
					uint64(i), // pc
					tc.opcode, // opcode
					100000,    // gas
					3,         // cost
					ctx,       // scope
					nil,       // rData
					1,         // depth
					nil,       // err
				)
			}
		})
	}
}

// BenchmarkOnOpcode_WithReturnData benchmarks OnOpcode with return data.
func BenchmarkOnOpcode_WithReturnData(b *testing.B) {
	returnData := make([]byte, 32) // Typical return data size
	for i := range returnData {
		returnData[i] = byte(i)
	}

	scenarios := []struct {
		name   string
		opcode byte
	}{
		{"RETURN", byte(vm.RETURN)},
		{"CALL", byte(vm.CALL)},
	}

	for _, tc := range scenarios {
		b.Run(tc.name, func(b *testing.B) {
			tracer := NewStructLogTracer(StructLogConfig{
				EnableReturnData: true,
			})
			ctx := newMockOpContext(10)

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				tracer.OnOpcode(
					uint64(i),
					tc.opcode,
					100000,
					0,
					ctx,
					returnData,
					1,
					nil,
				)
			}
		})
	}
}

// =============================================================================
// Simulated Transaction Benchmarks
// =============================================================================

// BenchmarkSimulatedTransaction_Small simulates a small transaction (~100 opcodes).
func BenchmarkSimulatedTransaction_Small(b *testing.B) {
	numOpcodes := 100
	callFrequency := 20 // 1 in 20 is a CALL

	opcodes := generateOpcodeSequence(numOpcodes, callFrequency)
	ctx := newMockOpContext(10)

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tracer := NewStructLogTracer(StructLogConfig{
			EnableReturnData: true,
		})

		for j, op := range opcodes {
			tracer.OnOpcode(uint64(j), op, 100000, 3, ctx, nil, 1, nil)
		}
	}
}

// BenchmarkSimulatedTransaction_Medium simulates a medium transaction (~1000 opcodes).
func BenchmarkSimulatedTransaction_Medium(b *testing.B) {
	numOpcodes := 1000
	callFrequency := 20

	opcodes := generateOpcodeSequence(numOpcodes, callFrequency)
	ctx := newMockOpContext(10)

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tracer := NewStructLogTracer(StructLogConfig{
			EnableReturnData: true,
		})

		for j, op := range opcodes {
			tracer.OnOpcode(uint64(j), op, 100000, 3, ctx, nil, 1, nil)
		}
	}
}

// BenchmarkSimulatedTransaction_Large simulates a large transaction (~10000 opcodes).
func BenchmarkSimulatedTransaction_Large(b *testing.B) {
	numOpcodes := 10000
	callFrequency := 20

	opcodes := generateOpcodeSequence(numOpcodes, callFrequency)
	ctx := newMockOpContext(10)

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tracer := NewStructLogTracer(StructLogConfig{
			EnableReturnData: true,
		})

		for j, op := range opcodes {
			tracer.OnOpcode(uint64(j), op, 100000, 3, ctx, nil, 1, nil)
		}
	}
}

// BenchmarkSimulatedTransaction_VeryLarge simulates a very large transaction (~100000 opcodes).
func BenchmarkSimulatedTransaction_VeryLarge(b *testing.B) {
	numOpcodes := 100000
	callFrequency := 20

	opcodes := generateOpcodeSequence(numOpcodes, callFrequency)
	ctx := newMockOpContext(10)

	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tracer := NewStructLogTracer(StructLogConfig{
			EnableReturnData: true,
		})

		for j, op := range opcodes {
			tracer.OnOpcode(uint64(j), op, 100000, 3, ctx, nil, 1, nil)
		}
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

// generateOpcodeSequence creates a realistic sequence of opcodes.
// callFrequency determines how often CALL opcodes appear (1 in N).
func generateOpcodeSequence(count, callFrequency int) []byte {
	opcodes := make([]byte, count)

	for i := range opcodes {
		if i%callFrequency == 0 {
			// Mix of CALL variants
			switch i % 4 {
			case 0:
				opcodes[i] = byte(vm.CALL)
			case 1:
				opcodes[i] = byte(vm.STATICCALL)
			case 2:
				opcodes[i] = byte(vm.DELEGATECALL)
			case 3:
				opcodes[i] = byte(vm.CALLCODE)
			}
		} else {
			// Common non-CALL opcodes
			switch i % 10 {
			case 0:
				opcodes[i] = byte(vm.PUSH1)
			case 1:
				opcodes[i] = byte(vm.PUSH2)
			case 2:
				opcodes[i] = byte(vm.DUP1)
			case 3:
				opcodes[i] = byte(vm.SWAP1)
			case 4:
				opcodes[i] = byte(vm.ADD)
			case 5:
				opcodes[i] = byte(vm.MLOAD)
			case 6:
				opcodes[i] = byte(vm.MSTORE)
			case 7:
				opcodes[i] = byte(vm.SLOAD)
			case 8:
				opcodes[i] = byte(vm.JUMP)
			case 9:
				opcodes[i] = byte(vm.JUMPI)
			}
		}
	}

	return opcodes
}
