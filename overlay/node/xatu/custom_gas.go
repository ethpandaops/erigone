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
	"github.com/erigontech/erigon/execution/protocol/params"
	"github.com/erigontech/erigon/execution/vm"
)

// CustomGasSchedule allows overriding gas costs for simulation.
// Keys are gas parameter names (e.g., GasKeySloadCold, or opcode names like "ADD").
// Any key not present uses the default value from the current fork.
type CustomGasSchedule struct {
	Overrides map[string]uint64 `json:"overrides,omitempty"`
}

// GasParameter represents a single gas parameter with its value and description.
type GasParameter struct {
	Value       uint64 `json:"value"`
	Description string `json:"description"`
}

// GasScheduleResponse is the API response for xatu_getGasSchedule.
type GasScheduleResponse struct {
	Parameters map[string]GasParameter `json:"parameters"`
}

// gasDescriptions maps gas parameter names to their descriptions.
var gasDescriptions = map[string]string{
	// Arithmetic
	"ADD":        "Addition. Fixed cost per operation.",
	"SUB":        "Subtraction. Fixed cost per operation.",
	"MUL":        "Multiplication. Fixed cost per operation.",
	"DIV":        "Unsigned division. Fixed cost per operation.",
	"SDIV":       "Signed division. Fixed cost per operation.",
	"MOD":        "Unsigned modulo. Fixed cost per operation.",
	"SMOD":       "Signed modulo. Fixed cost per operation.",
	"ADDMOD":     "Modular addition: (a + b) % N. Fixed cost.",
	"MULMOD":     "Modular multiplication: (a × b) % N. Fixed cost.",
	"EXP_BYTE":   "Cost per byte of the exponent in EXP. Total cost = 10 + (EXP_BYTE × exponent_bytes).",
	"SIGNEXTEND": "Sign-extend a smaller signed integer. Fixed cost.",

	// Comparison & Bitwise
	"LT":     "Less-than comparison. Fixed cost.",
	"GT":     "Greater-than comparison. Fixed cost.",
	"SLT":    "Signed less-than comparison. Fixed cost.",
	"SGT":    "Signed greater-than comparison. Fixed cost.",
	"EQ":     "Equality comparison. Fixed cost.",
	"ISZERO": "Check if value is zero. Fixed cost.",
	"AND":    "Bitwise AND. Fixed cost.",
	"OR":     "Bitwise OR. Fixed cost.",
	"XOR":    "Bitwise XOR. Fixed cost.",
	"NOT":    "Bitwise NOT. Fixed cost.",
	"BYTE":   "Extract single byte from word. Fixed cost.",
	"SHL":    "Shift left. Fixed cost.",
	"SHR":    "Logical shift right. Fixed cost.",
	"SAR":    "Arithmetic shift right (preserves sign). Fixed cost.",
	"CLZ":    "Count leading zeros. Fixed cost.",

	// Stack Operations
	"POP":    "Remove top stack item. Fixed cost.",
	"PUSH0":  "Push zero onto stack. Fixed cost.",
	"PUSH1":  "Push 1-byte value onto stack. Fixed cost.",
	"PUSH2":  "Push 2-byte value onto stack. Fixed cost.",
	"PUSH3":  "Push 3-byte value onto stack. Fixed cost.",
	"PUSH4":  "Push 4-byte value onto stack. Fixed cost.",
	"PUSH5":  "Push 5-byte value onto stack. Fixed cost.",
	"PUSH6":  "Push 6-byte value onto stack. Fixed cost.",
	"PUSH7":  "Push 7-byte value onto stack. Fixed cost.",
	"PUSH8":  "Push 8-byte value onto stack. Fixed cost.",
	"PUSH9":  "Push 9-byte value onto stack. Fixed cost.",
	"PUSH10": "Push 10-byte value onto stack. Fixed cost.",
	"PUSH11": "Push 11-byte value onto stack. Fixed cost.",
	"PUSH12": "Push 12-byte value onto stack. Fixed cost.",
	"PUSH13": "Push 13-byte value onto stack. Fixed cost.",
	"PUSH14": "Push 14-byte value onto stack. Fixed cost.",
	"PUSH15": "Push 15-byte value onto stack. Fixed cost.",
	"PUSH16": "Push 16-byte value onto stack. Fixed cost.",
	"PUSH17": "Push 17-byte value onto stack. Fixed cost.",
	"PUSH18": "Push 18-byte value onto stack. Fixed cost.",
	"PUSH19": "Push 19-byte value onto stack. Fixed cost.",
	"PUSH20": "Push 20-byte value onto stack. Fixed cost.",
	"PUSH21": "Push 21-byte value onto stack. Fixed cost.",
	"PUSH22": "Push 22-byte value onto stack. Fixed cost.",
	"PUSH23": "Push 23-byte value onto stack. Fixed cost.",
	"PUSH24": "Push 24-byte value onto stack. Fixed cost.",
	"PUSH25": "Push 25-byte value onto stack. Fixed cost.",
	"PUSH26": "Push 26-byte value onto stack. Fixed cost.",
	"PUSH27": "Push 27-byte value onto stack. Fixed cost.",
	"PUSH28": "Push 28-byte value onto stack. Fixed cost.",
	"PUSH29": "Push 29-byte value onto stack. Fixed cost.",
	"PUSH30": "Push 30-byte value onto stack. Fixed cost.",
	"PUSH31": "Push 31-byte value onto stack. Fixed cost.",
	"PUSH32": "Push 32-byte value onto stack. Fixed cost.",
	"DUP1":   "Duplicate 1st stack item. Fixed cost.",
	"DUP2":   "Duplicate 2nd stack item. Fixed cost.",
	"DUP3":   "Duplicate 3rd stack item. Fixed cost.",
	"DUP4":   "Duplicate 4th stack item. Fixed cost.",
	"DUP5":   "Duplicate 5th stack item. Fixed cost.",
	"DUP6":   "Duplicate 6th stack item. Fixed cost.",
	"DUP7":   "Duplicate 7th stack item. Fixed cost.",
	"DUP8":   "Duplicate 8th stack item. Fixed cost.",
	"DUP9":   "Duplicate 9th stack item. Fixed cost.",
	"DUP10":  "Duplicate 10th stack item. Fixed cost.",
	"DUP11":  "Duplicate 11th stack item. Fixed cost.",
	"DUP12":  "Duplicate 12th stack item. Fixed cost.",
	"DUP13":  "Duplicate 13th stack item. Fixed cost.",
	"DUP14":  "Duplicate 14th stack item. Fixed cost.",
	"DUP15":  "Duplicate 15th stack item. Fixed cost.",
	"DUP16":  "Duplicate 16th stack item. Fixed cost.",
	"SWAP1":  "Swap top with 2nd stack item. Fixed cost.",
	"SWAP2":  "Swap top with 3rd stack item. Fixed cost.",
	"SWAP3":  "Swap top with 4th stack item. Fixed cost.",
	"SWAP4":  "Swap top with 5th stack item. Fixed cost.",
	"SWAP5":  "Swap top with 6th stack item. Fixed cost.",
	"SWAP6":  "Swap top with 7th stack item. Fixed cost.",
	"SWAP7":  "Swap top with 8th stack item. Fixed cost.",
	"SWAP8":  "Swap top with 9th stack item. Fixed cost.",
	"SWAP9":  "Swap top with 10th stack item. Fixed cost.",
	"SWAP10": "Swap top with 11th stack item. Fixed cost.",
	"SWAP11": "Swap top with 12th stack item. Fixed cost.",
	"SWAP12": "Swap top with 13th stack item. Fixed cost.",
	"SWAP13": "Swap top with 14th stack item. Fixed cost.",
	"SWAP14": "Swap top with 15th stack item. Fixed cost.",
	"SWAP15": "Swap top with 16th stack item. Fixed cost.",
	"SWAP16": "Swap top with 17th stack item. Fixed cost.",

	// Memory
	"MLOAD":   "Load 32 bytes from memory. Base cost only; memory expansion charged separately via MEMORY.",
	"MSTORE":  "Store 32 bytes to memory. Base cost only; memory expansion charged separately via MEMORY.",
	"MSTORE8": "Store 1 byte to memory. Base cost only; memory expansion charged separately via MEMORY.",
	"MSIZE":   "Get current memory size in bytes. Fixed cost.",
	"MCOPY":   "Copy memory regions. Base cost; also uses COPY for per-word cost and MEMORY for expansion.",
	"MEMORY":  "Linear coefficient for memory expansion. Total cost = MEMORY × words + words²÷512. Only the linear part is configurable; the quadratic part is fixed.",
	"COPY":    "Per-word cost for memory copy operations (CALLDATACOPY, CODECOPY, EXTCODECOPY, RETURNDATACOPY, MCOPY).",

	// Storage
	"SLOAD_COLD":   "Reading storage slot for first time in transaction. Post-Berlin (EIP-2929).",
	"SLOAD_WARM":   "Reading storage slot already accessed in transaction. Post-Berlin (EIP-2929).",
	"SSTORE_SET":   "Writing to a storage slot that was zero (creating new storage).",
	"SSTORE_RESET": "Writing to a storage slot that was non-zero (modifying existing storage).",

	// Transient Storage
	"TLOAD":  "Load from transient storage. Cleared after transaction. (EIP-1153)",
	"TSTORE": "Store to transient storage. Cleared after transaction. (EIP-1153)",

	// Contract Calls
	"CALL":             "Base cost for CALL. This is the warm access cost; first access to an address adds CALL_COLD.",
	"CALLCODE":         "Base cost for CALLCODE. This is the warm access cost; first access to an address adds CALL_COLD.",
	"DELEGATECALL":     "Base cost for DELEGATECALL. This is the warm access cost; first access to an address adds CALL_COLD.",
	"STATICCALL":       "Base cost for STATICCALL. This is the warm access cost; first access to an address adds CALL_COLD.",
	"CALL_COLD":        "Additional cost when calling an address not yet accessed in transaction. Post-Berlin (EIP-2929).",
	"CALL_VALUE_XFER":  "Additional cost when CALL transfers ETH value.",
	"CALL_NEW_ACCOUNT": "Additional cost when CALL sends value to a non-existent account, creating it.",

	// Contract Creation
	"CREATE":                 "Base cost for CREATE. Additional costs: INIT_CODE_WORD per word of init code, memory expansion, and code deposit (200 gas per byte stored).",
	"CREATE2":                "Base cost for CREATE2. Additional costs: INIT_CODE_WORD, KECCAK256_WORD for address derivation, memory expansion, and code deposit.",
	"INIT_CODE_WORD":         "Per-word cost for contract init code in CREATE/CREATE2. (EIP-3860)",
	"CREATE_BY_SELFDESTRUCT": "Cost when SELFDESTRUCT sends funds to non-existent account, creating it.",

	// External Code
	"EXTCODESIZE": "Get code size of external account. Base cost; first access to address adds CALL_COLD.",
	"EXTCODECOPY": "Copy external account code to memory. Base cost; uses COPY for per-word cost, MEMORY for expansion. First access adds CALL_COLD.",
	"EXTCODEHASH": "Get code hash of external account. Base cost; first access to address adds CALL_COLD.",
	"CODESIZE":    "Get size of current contract's code. Fixed cost.",
	"CODECOPY":    "Copy current contract's code to memory. Base cost; uses COPY for per-word cost and MEMORY for expansion.",

	// Call Data
	"CALLDATALOAD":   "Load 32 bytes from call input data. Fixed cost.",
	"CALLDATASIZE":   "Get size of call input data. Fixed cost.",
	"CALLDATACOPY":   "Copy call input data to memory. Base cost; uses COPY for per-word cost and MEMORY for expansion.",
	"RETURNDATASIZE": "Get size of return data from last external call. Fixed cost.",
	"RETURNDATACOPY": "Copy return data to memory. Base cost; uses COPY for per-word cost and MEMORY for expansion.",

	// Block Information
	"BLOCKHASH":   "Get hash of one of the 256 most recent blocks. Fixed cost.",
	"COINBASE":    "Get current block's beneficiary address. Fixed cost.",
	"TIMESTAMP":   "Get current block's timestamp. Fixed cost.",
	"NUMBER":      "Get current block number. Fixed cost.",
	"DIFFICULTY":  "Get current block's difficulty (prevrandao post-merge). Fixed cost.",
	"GASLIMIT":    "Get current block's gas limit. Fixed cost.",
	"CHAINID":     "Get chain ID. Fixed cost.",
	"BASEFEE":     "Get current block's base fee. Fixed cost. (EIP-1559)",
	"BLOBBASEFEE": "Get current block's blob base fee. Fixed cost. (EIP-4844)",
	"BLOBHASH":    "Get versioned hash of blob at given index. Fixed cost. (EIP-4844)",

	// Account Information
	"BALANCE":     "Get account balance. Base cost; first access to address adds CALL_COLD.",
	"SELFBALANCE": "Get current contract's balance. Fixed cost (always warm).",
	"ORIGIN":      "Get transaction origin address (tx.origin). Fixed cost.",
	"CALLER":      "Get direct caller address (msg.sender). Fixed cost.",
	"CALLVALUE":   "Get ETH value sent with call (msg.value). Fixed cost.",
	"ADDRESS":     "Get current contract's address. Fixed cost.",
	"GASPRICE":    "Get gas price of current transaction. Fixed cost.",
	"GAS":         "Get remaining gas. Fixed cost.",

	// Control Flow
	"JUMP":     "Unconditional jump to destination. Fixed cost.",
	"JUMPI":    "Conditional jump if condition is non-zero. Fixed cost.",
	"JUMPDEST": "Valid destination for jumps. Fixed cost.",
	"PC":       "Get program counter before this instruction. Fixed cost.",
	"STOP":     "Halt execution, returning no data. Fixed cost.",
	"RETURN":   "Halt execution, returning memory data. Base cost; memory expansion charged via MEMORY.",
	"REVERT":   "Halt execution, revert state changes, return data. Base cost; memory expansion charged via MEMORY.",
	"INVALID":  "Designated invalid instruction. Consumes all remaining gas.",

	// Logging
	"LOG0":      "Append log with 0 topics. Uses LOG base + LOG_DATA per byte.",
	"LOG1":      "Append log with 1 topic. Uses LOG base + LOG_TOPIC + LOG_DATA per byte.",
	"LOG2":      "Append log with 2 topics. Uses LOG base + 2×LOG_TOPIC + LOG_DATA per byte.",
	"LOG3":      "Append log with 3 topics. Uses LOG base + 3×LOG_TOPIC + LOG_DATA per byte.",
	"LOG4":      "Append log with 4 topics. Uses LOG base + 4×LOG_TOPIC + LOG_DATA per byte.",
	"LOG":       "Base cost for all LOG operations.",
	"LOG_TOPIC": "Additional cost per topic in LOG1-LOG4.",
	"LOG_DATA":  "Per-byte cost for log data.",

	// Hashing
	"KECCAK256":      "Base cost for KECCAK256 hash operation.",
	"KECCAK256_WORD": "Per-word (32 bytes) cost for data being hashed.",

	// Self-destruct
	"SELFDESTRUCT": "Mark contract for destruction. Base cost; adds CALL_COLD if recipient is cold, CREATE_BY_SELFDESTRUCT if recipient doesn't exist.",
}

// GasScheduleForRules returns default gas values for a fork.
// Used internally by GasScheduleResponseForRules() for the API response.
//
// NOTE: Constant gas opcodes come from JumpTable (auto-updated per fork).
// Dynamic gas defaults are hardcoded here - if a future EIP changes them,
// this function needs updating (like we did for EXP_BYTE in Spurious Dragon).
func GasScheduleForRules(rules *chain.Rules) *CustomGasSchedule {
	schedule := &CustomGasSchedule{Overrides: make(map[string]uint64)}

	// Constant gas from JumpTable (valid opcodes for this fork)
	jt := vm.GetBaseJumpTable(rules)
	for i := 0; i < 256; i++ {
		opcode := vm.OpCode(i)
		if op := jt[opcode]; op != nil {
			if gas := op.GetConstantGas(); gas > 0 || opcode == vm.STOP || opcode == vm.JUMPDEST {
				schedule.Overrides[opcode.String()] = gas
			}
		}
	}

	// Dynamic gas defaults
	schedule.Overrides[vm.GasKeyMemory] = params.MemoryGas
	schedule.Overrides[vm.GasKeyCopy] = params.CopyGas
	schedule.Overrides[vm.GasKeyKeccak256Word] = params.Keccak256WordGas
	schedule.Overrides[vm.GasKeyLog] = params.LogGas
	schedule.Overrides[vm.GasKeyLogTopic] = params.LogTopicGas
	schedule.Overrides[vm.GasKeyLogData] = params.LogDataGas
	schedule.Overrides[vm.GasKeyCallValueXfer] = params.CallValueTransferGas
	schedule.Overrides[vm.GasKeyCallNewAccount] = params.CallNewAccountGas
	schedule.Overrides[vm.GasKeyCreateBySelfDestruct] = params.CreateBySelfdestructGas
	schedule.Overrides[vm.GasKeyInitCodeWord] = params.InitCodeWordGas

	// Fork-specific defaults
	if rules.IsSpuriousDragon {
		schedule.Overrides[vm.GasKeyExpByte] = params.ExpByteEIP160
	} else {
		schedule.Overrides[vm.GasKeyExpByte] = params.ExpByteFrontier
	}

	if rules.IsBerlin {
		schedule.Overrides[vm.GasKeySloadCold] = params.ColdSloadCostEIP2929
		schedule.Overrides[vm.GasKeySloadWarm] = params.WarmStorageReadCostEIP2929
		schedule.Overrides[vm.GasKeyCallCold] = params.ColdAccountAccessCostEIP2929
		// Note: CALL_WARM is intentionally omitted from API response.
		// The warm cost for CALL variants is controlled by their JumpTable constant gas
		// (CALL, STATICCALL, DELEGATECALL, CALLCODE sliders). CALL_WARM only affects
		// the cold surcharge calculation (CALL_COLD - CALL_WARM), which is confusing
		// for users. Exposing only CALL_COLD keeps the mental model simple:
		// - CALL/STATICCALL/etc sliders = warm cost
		// - CALL_COLD = cold cost
		delete(schedule.Overrides, vm.SLOAD.String())
	}

	if rules.IsIstanbul {
		schedule.Overrides[vm.GasKeySstoreSet] = params.SstoreSetGasEIP2200
		schedule.Overrides[vm.GasKeySstoreReset] = params.SstoreResetGasEIP2200
	}

	return schedule
}

// GasScheduleResponseForRules returns gas parameters with values and descriptions for a fork.
// This is the response format for the xatu_getGasSchedule API.
func GasScheduleResponseForRules(rules *chain.Rules) *GasScheduleResponse {
	schedule := GasScheduleForRules(rules)
	response := &GasScheduleResponse{
		Parameters: make(map[string]GasParameter, len(schedule.Overrides)),
	}

	for name, value := range schedule.Overrides {
		desc := gasDescriptions[name]
		if desc == "" {
			desc = "Gas cost for " + name + " operation."
		}
		response.Parameters[name] = GasParameter{
			Value:       value,
			Description: desc,
		}
	}

	return response
}

// HasOverrides returns true if any custom values have been set.
func (c *CustomGasSchedule) HasOverrides() bool {
	return c != nil && len(c.Overrides) > 0
}

// ToVMGasSchedule converts CustomGasSchedule to vm.GasSchedule.
// The vm.GasSchedule is used by patched gas functions via GetOr().
func (c *CustomGasSchedule) ToVMGasSchedule() *vm.GasSchedule {
	if c == nil || len(c.Overrides) == 0 {
		return nil
	}
	return &vm.GasSchedule{Overrides: c.Overrides}
}
