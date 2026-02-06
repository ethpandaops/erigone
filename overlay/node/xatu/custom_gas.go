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

// GasScheduleForRules returns default gas values for a fork (for API display only).
// Execution uses patched gas functions with hardcoded fallbacks via GetOr().
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
		schedule.Overrides[vm.GasKeyCallWarm] = params.WarmStorageReadCostEIP2929
		delete(schedule.Overrides, vm.SLOAD.String())
	}

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
