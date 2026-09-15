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

import "github.com/erigontech/erigon/execution/vm"

// The v3 jump table stores instruction pointers; an opcode is present when
// its slot is non-nil.

// constantGasEntries calls visit with the constant gas of every instruction
// the fork defines.
func constantGasEntries(jt *vm.JumpTable, visit func(opcode vm.OpCode, gas uint64)) {
	for i := 0; i < 256; i++ {
		if op := jt[i]; op != nil {
			visit(vm.OpCode(i), op.GetConstantGas())
		}
	}
}

// setConstantGas overrides the constant gas of a defined instruction and
// leaves opcodes the fork does not define untouched.
func setConstantGas(jt *vm.JumpTable, opcode vm.OpCode, gas uint64) {
	if op := jt[opcode]; op != nil {
		op.SetConstantGas(gas)
	}
}
