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
var opcodeMap = map[string]vm.OpCode{
	"STOP":           vm.STOP,
	"ADD":            vm.ADD,
	"MUL":            vm.MUL,
	"SUB":            vm.SUB,
	"DIV":            vm.DIV,
	"SDIV":           vm.SDIV,
	"MOD":            vm.MOD,
	"SMOD":           vm.SMOD,
	"ADDMOD":         vm.ADDMOD,
	"MULMOD":         vm.MULMOD,
	"EXP":            vm.EXP,
	"SIGNEXTEND":     vm.SIGNEXTEND,
	"LT":             vm.LT,
	"GT":             vm.GT,
	"SLT":            vm.SLT,
	"SGT":            vm.SGT,
	"EQ":             vm.EQ,
	"ISZERO":         vm.ISZERO,
	"AND":            vm.AND,
	"OR":             vm.OR,
	"XOR":            vm.XOR,
	"NOT":            vm.NOT,
	"BYTE":           vm.BYTE,
	"SHL":            vm.SHL,
	"SHR":            vm.SHR,
	"SAR":            vm.SAR,
	"KECCAK256":      vm.KECCAK256,
	"ADDRESS":        vm.ADDRESS,
	"BALANCE":        vm.BALANCE,
	"ORIGIN":         vm.ORIGIN,
	"CALLER":         vm.CALLER,
	"CALLVALUE":      vm.CALLVALUE,
	"CALLDATALOAD":   vm.CALLDATALOAD,
	"CALLDATASIZE":   vm.CALLDATASIZE,
	"CALLDATACOPY":   vm.CALLDATACOPY,
	"CODESIZE":       vm.CODESIZE,
	"CODECOPY":       vm.CODECOPY,
	"GASPRICE":       vm.GASPRICE,
	"EXTCODESIZE":    vm.EXTCODESIZE,
	"EXTCODECOPY":    vm.EXTCODECOPY,
	"RETURNDATASIZE": vm.RETURNDATASIZE,
	"RETURNDATACOPY": vm.RETURNDATACOPY,
	"EXTCODEHASH":    vm.EXTCODEHASH,
	"BLOCKHASH":      vm.BLOCKHASH,
	"COINBASE":       vm.COINBASE,
	"TIMESTAMP":      vm.TIMESTAMP,
	"NUMBER":         vm.NUMBER,
	"DIFFICULTY":     vm.DIFFICULTY,
	"GASLIMIT":       vm.GASLIMIT,
	"CHAINID":        vm.CHAINID,
	"SELFBALANCE":    vm.SELFBALANCE,
	"BASEFEE":        vm.BASEFEE,
	"BLOBHASH":       vm.BLOBHASH,
	"BLOBBASEFEE":    vm.BLOBBASEFEE,
	"POP":            vm.POP,
	"MLOAD":          vm.MLOAD,
	"MSTORE":         vm.MSTORE,
	"MSTORE8":        vm.MSTORE8,
	"SLOAD":          vm.SLOAD,
	"SSTORE":         vm.SSTORE,
	"JUMP":           vm.JUMP,
	"JUMPI":          vm.JUMPI,
	"PC":             vm.PC,
	"MSIZE":          vm.MSIZE,
	"GAS":            vm.GAS,
	"JUMPDEST":       vm.JUMPDEST,
	"TLOAD":          vm.TLOAD,
	"TSTORE":         vm.TSTORE,
	"MCOPY":          vm.MCOPY,
	"PUSH0":          vm.PUSH0,
	"PUSH1":          vm.PUSH1,
	"PUSH2":          vm.PUSH2,
	"PUSH3":          vm.PUSH3,
	"PUSH4":          vm.PUSH4,
	"PUSH5":          vm.PUSH5,
	"PUSH6":          vm.PUSH6,
	"PUSH7":          vm.PUSH7,
	"PUSH8":          vm.PUSH8,
	"PUSH9":          vm.PUSH9,
	"PUSH10":         vm.PUSH10,
	"PUSH11":         vm.PUSH11,
	"PUSH12":         vm.PUSH12,
	"PUSH13":         vm.PUSH13,
	"PUSH14":         vm.PUSH14,
	"PUSH15":         vm.PUSH15,
	"PUSH16":         vm.PUSH16,
	"PUSH17":         vm.PUSH17,
	"PUSH18":         vm.PUSH18,
	"PUSH19":         vm.PUSH19,
	"PUSH20":         vm.PUSH20,
	"PUSH21":         vm.PUSH21,
	"PUSH22":         vm.PUSH22,
	"PUSH23":         vm.PUSH23,
	"PUSH24":         vm.PUSH24,
	"PUSH25":         vm.PUSH25,
	"PUSH26":         vm.PUSH26,
	"PUSH27":         vm.PUSH27,
	"PUSH28":         vm.PUSH28,
	"PUSH29":         vm.PUSH29,
	"PUSH30":         vm.PUSH30,
	"PUSH31":         vm.PUSH31,
	"PUSH32":         vm.PUSH32,
	"DUP1":           vm.DUP1,
	"DUP2":           vm.DUP2,
	"DUP3":           vm.DUP3,
	"DUP4":           vm.DUP4,
	"DUP5":           vm.DUP5,
	"DUP6":           vm.DUP6,
	"DUP7":           vm.DUP7,
	"DUP8":           vm.DUP8,
	"DUP9":           vm.DUP9,
	"DUP10":          vm.DUP10,
	"DUP11":          vm.DUP11,
	"DUP12":          vm.DUP12,
	"DUP13":          vm.DUP13,
	"DUP14":          vm.DUP14,
	"DUP15":          vm.DUP15,
	"DUP16":          vm.DUP16,
	"SWAP1":          vm.SWAP1,
	"SWAP2":          vm.SWAP2,
	"SWAP3":          vm.SWAP3,
	"SWAP4":          vm.SWAP4,
	"SWAP5":          vm.SWAP5,
	"SWAP6":          vm.SWAP6,
	"SWAP7":          vm.SWAP7,
	"SWAP8":          vm.SWAP8,
	"SWAP9":          vm.SWAP9,
	"SWAP10":         vm.SWAP10,
	"SWAP11":         vm.SWAP11,
	"SWAP12":         vm.SWAP12,
	"SWAP13":         vm.SWAP13,
	"SWAP14":         vm.SWAP14,
	"SWAP15":         vm.SWAP15,
	"SWAP16":         vm.SWAP16,
	"LOG0":           vm.LOG0,
	"LOG1":           vm.LOG1,
	"LOG2":           vm.LOG2,
	"LOG3":           vm.LOG3,
	"LOG4":           vm.LOG4,
	"CREATE":         vm.CREATE,
	"CALL":           vm.CALL,
	"CALLCODE":       vm.CALLCODE,
	"RETURN":         vm.RETURN,
	"DELEGATECALL":   vm.DELEGATECALL,
	"CREATE2":        vm.CREATE2,
	"STATICCALL":     vm.STATICCALL,
	"REVERT":         vm.REVERT,
	"INVALID":        vm.INVALID,
	"SELFDESTRUCT":   vm.SELFDESTRUCT,
}
