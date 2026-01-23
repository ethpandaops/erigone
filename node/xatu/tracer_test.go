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

	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/execution/vm"
)

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
