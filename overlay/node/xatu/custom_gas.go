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

// GasScheduleForRules returns a CustomGasSchedule with default values
// appropriate for the given chain rules/fork.
// Values are extracted directly from the fork's JumpTable to ensure accuracy.
// All valid opcodes for the fork are included automatically - no manual list to maintain.
func GasScheduleForRules(rules *chain.Rules) *CustomGasSchedule {
	schedule := &CustomGasSchedule{
		Overrides: make(map[string]uint64),
	}

	// Get the JumpTable for this fork - this gives us accurate gas costs
	jt := vm.GetBaseJumpTable(rules)

	// Extract constant gas costs from the JumpTable for ALL opcodes
	// This automatically includes only opcodes valid for this fork
	for i := 0; i < 256; i++ {
		opcode := vm.OpCode(i)
		op := jt[opcode]
		if op != nil {
			gas := op.GetConstantGas()
			// Include if gas > 0, or if it's an explicitly free opcode (STOP, JUMPDEST)
			if gas > 0 || opcode == vm.STOP || opcode == vm.JUMPDEST {
				schedule.Overrides[opcode.String()] = gas
			}
		}
	}

	// === Dynamic gas components ===
	//
	// These values are used in dynamic gas functions (e.g., memoryGasCost, memoryCopierGas)
	// but are not stored in the JumpTable's constantGas field.
	//
	// IMPORTANT: These params constants have NOT changed across any Ethereum fork to date.
	// Erigon's dynamic gas functions reference them directly without fork checks.
	// If a future EIP changes these values, both Erigon and this code would need updates
	// to add fork-aware logic (similar to how EXP_BYTE is handled below).
	//
	// Values that HAVE changed across forks use explicit fork checks (see below).

	// Memory gas (used by memory expansion calculation in memoryGasCost)
	schedule.Overrides[vm.GasKeyMemory] = params.MemoryGas

	// Copy gas (per-word cost for CALLDATACOPY, CODECOPY, etc. in memoryCopierGas)
	schedule.Overrides[vm.GasKeyCopy] = params.CopyGas

	// Keccak256 per-word cost
	schedule.Overrides[vm.GasKeyKeccak256Word] = params.Keccak256WordGas

	// Log costs
	schedule.Overrides[vm.GasKeyLog] = params.LogGas
	schedule.Overrides[vm.GasKeyLogTopic] = params.LogTopicGas
	schedule.Overrides[vm.GasKeyLogData] = params.LogDataGas

	// Call costs (unchanged since Frontier)
	schedule.Overrides[vm.GasKeyCallValueXfer] = params.CallValueTransferGas
	schedule.Overrides[vm.GasKeyCallNewAccount] = params.CallNewAccountGas

	// Create/Selfdestruct costs
	schedule.Overrides[vm.GasKeyCreateBySelfDestruct] = params.CreateBySelfdestructGas
	schedule.Overrides[vm.GasKeyInitCodeWord] = params.InitCodeWordGas

	// === Fork-specific dynamic gas components ===
	//
	// These values HAVE changed across forks, so we check chain rules.

	// EXP byte cost: Changed in EIP-160 (Spurious Dragon)
	if rules.IsSpuriousDragon {
		schedule.Overrides[vm.GasKeyExpByte] = params.ExpByteEIP160
	} else {
		schedule.Overrides[vm.GasKeyExpByte] = params.ExpByteFrontier
	}

	// EIP-2929 (Berlin+): Cold/warm access costs replace flat costs
	if rules.IsBerlin {
		schedule.Overrides[vm.GasKeySloadCold] = params.ColdSloadCostEIP2929
		schedule.Overrides[vm.GasKeySloadWarm] = params.WarmStorageReadCostEIP2929
		schedule.Overrides[vm.GasKeyCallCold] = params.ColdAccountAccessCostEIP2929
		schedule.Overrides[vm.GasKeyCallWarm] = params.WarmStorageReadCostEIP2929
		// Remove the single SLOAD cost since Berlin uses cold/warm
		delete(schedule.Overrides, vm.SLOAD.String())
	}

	// EIP-2200 (Istanbul+): SSTORE costs
	if rules.IsIstanbul {
		schedule.Overrides[vm.GasKeySstoreSet] = params.SstoreSetGasEIP2200
		schedule.Overrides[vm.GasKeySstoreReset] = params.SstoreResetGasEIP2200
	}

	return schedule
}

// Get returns the custom value if set, or the default value.
func (c *CustomGasSchedule) Get(key string, defaultVal uint64) uint64 {
	if c != nil && c.Overrides != nil {
		if val, ok := c.Overrides[key]; ok {
			return val
		}
	}
	return defaultVal
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
