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
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/vm"
)

// BuildCustomJumpTable creates a custom JumpTable with constant gas costs overridden.
// Dynamic gas overrides (SLOAD, SSTORE, CALL, etc.) are handled by setting evm.GasSchedule
// which the patched gas functions read via GetOr().
func BuildCustomJumpTable(chainRules *chain.Rules, schedule *CustomGasSchedule) *vm.JumpTable {
	if schedule == nil || !schedule.HasOverrides() {
		return vm.GetBaseJumpTable(chainRules)
	}

	jt := vm.GetBaseJumpTable(chainRules)

	// Apply constant-gas opcode overrides only
	// Dynamic gas (SLOAD, SSTORE, CALL, etc.) is handled by evm.GasSchedule
	for opcodeName, gas := range schedule.Overrides {
		opcode, ok := opcodeFromString(opcodeName)
		if !ok {
			continue // Not a direct opcode name (e.g., SLOAD_COLD)
		}
		if jt[opcode] != nil {
			jt[opcode].SetConstantGas(gas)
		}
	}

	return jt
}

// opcodeFromString converts an opcode name string to vm.OpCode.
func opcodeFromString(name string) (vm.OpCode, bool) {
	op, ok := opcodeMap[name]
	return op, ok
}

// opcodeMap maps opcode names to their OpCode values.
// Built dynamically from Erigon's OpCode.String() to avoid hardcoded list.
var opcodeMap = func() map[string]vm.OpCode {
	m := make(map[string]vm.OpCode)
	for i := 0; i < 256; i++ {
		op := vm.OpCode(i)
		name := op.String()
		// Skip invalid/undefined opcodes (they return "0xNN" format)
		if len(name) > 0 && name[0] != '0' {
			m[name] = op
		}
	}
	return m
}()
