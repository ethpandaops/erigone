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
	"errors"

	"github.com/holiman/uint256"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/common/math"
	"github.com/erigontech/erigon/execution/chain"
	"github.com/erigontech/erigon/execution/protocol/params"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/erigontech/erigon/execution/vm"
)

// BuildCustomJumpTable creates a custom JumpTable with gas costs overridden
// according to the provided CustomGasSchedule.
// It copies the base JumpTable for the given chain rules and applies overrides.
func BuildCustomJumpTable(chainRules *chain.Rules, schedule *CustomGasSchedule) *vm.JumpTable {
	if schedule == nil || !schedule.HasOverrides() {
		return vm.GetBaseJumpTable(chainRules)
	}

	jt := vm.GetBaseJumpTable(chainRules)

	// Apply all overrides
	applyOverrides(jt, schedule)

	return jt
}

// has checks if a key exists in the schedule's overrides map.
func has(schedule *CustomGasSchedule, key string) bool {
	if schedule == nil || schedule.Overrides == nil {
		return false
	}
	_, ok := schedule.Overrides[key]
	return ok
}

// get returns the value for a key, or the default if not present.
func get(schedule *CustomGasSchedule, key string, defaultVal uint64) uint64 {
	return schedule.Get(key, defaultVal)
}

// applyOverrides applies all gas overrides from the schedule.
func applyOverrides(jt *vm.JumpTable, schedule *CustomGasSchedule) {
	// NOTE: MemoryGas cannot be customized - it's calculated at the interpreter level
	// before dynamic gas functions are called. Both would conflict on Memory.lastGasCost.

	// Apply simple constant-gas opcode overrides
	for opcodeName, gas := range schedule.Overrides {
		opcode, ok := opcodeFromString(opcodeName)
		if !ok {
			continue // Not a direct opcode name, might be a compound key like SLOAD_COLD
		}
		if jt[opcode] != nil {
			jt[opcode].SetConstantGas(gas)
		}
	}

	// SLOAD - uses dynamic gas for warm/cold access (EIP-2929)
	// VERIFIED: Matches gasSLoadEIP2929 in execution/vm/operations_acl.go:106-114
	if has(schedule, GasKeySloadCold) || has(schedule, GasKeySloadWarm) {
		if jt[vm.SLOAD] != nil {
			coldCost := get(schedule, GasKeySloadCold, params.ColdSloadCostEIP2929)
			warmCost := get(schedule, GasKeySloadWarm, params.WarmStorageReadCostEIP2929)
			jt[vm.SLOAD].SetDynamicGas(makeCustomSloadGas(coldCost, warmCost))
		}
	}

	// SSTORE - uses dynamic gas for set/reset/clear (EIP-2929 + EIP-2200)
	// VERIFIED: Matches makeGasSStoreFunc in execution/vm/operations_acl.go:33-99
	if has(schedule, GasKeySstoreSet) || has(schedule, GasKeySstoreReset) || has(schedule, GasKeySloadCold) || has(schedule, GasKeySloadWarm) {
		if jt[vm.SSTORE] != nil {
			coldSloadCost := get(schedule, GasKeySloadCold, params.ColdSloadCostEIP2929)
			warmReadCost := get(schedule, GasKeySloadWarm, params.WarmStorageReadCostEIP2929)
			setGas := get(schedule, GasKeySstoreSet, params.SstoreSetGasEIP2200)
			resetGas := get(schedule, GasKeySstoreReset, params.SstoreResetGasEIP2200)

			// Calculate clearing refund: SSTORE_RESET_GAS - COLD_SLOAD_COST + ACCESS_LIST_STORAGE_KEY_GAS
			// This matches params.SstoreClearsScheduleRefundEIP3529 calculation
			clearingRefund := calculateClearingRefund(resetGas, coldSloadCost)

			p := &sstoreGasParams{
				coldSloadCost:  coldSloadCost,
				warmReadCost:   warmReadCost,
				setGas:         setGas,
				resetGas:       resetGas,
				clearingRefund: clearingRefund,
				sentryGas:      params.SstoreSentryGasEIP2200,
			}
			jt[vm.SSTORE].SetDynamicGas(makeCustomSstoreGas(p))
		}
	}

	// EXP - uses dynamic gas for base + byte cost
	// VERIFIED: Matches gasExpEIP160 in execution/vm/gas_table.go:368-379
	if has(schedule, vm.EXP.String()) || has(schedule, GasKeyExpByte) {
		if jt[vm.EXP] != nil {
			baseGas := get(schedule, vm.EXP.String(), params.ExpGas)
			byteGas := get(schedule, GasKeyExpByte, params.ExpByteEIP160)
			jt[vm.EXP].SetDynamicGas(makeCustomExpGas(baseGas, byteGas))
		}
	}

	// KECCAK256 - base is constant, word cost is dynamic
	// VERIFIED: Matches gasKeccak256 in execution/vm/gas_table.go:256-272
	if has(schedule, vm.KECCAK256.String()) {
		if jt[vm.KECCAK256] != nil {
			jt[vm.KECCAK256].SetConstantGas(get(schedule, vm.KECCAK256.String(), params.Keccak256Gas))
		}
	}
	if has(schedule, GasKeyKeccak256Word) {
		if jt[vm.KECCAK256] != nil {
			jt[vm.KECCAK256].SetDynamicGas(makeCustomKeccak256Gas(get(schedule, GasKeyKeccak256Word, params.Keccak256WordGas)))
		}
	}

	// LOG0-4 - uses dynamic gas for base + topics + data
	// VERIFIED: Matches makeGasLog in execution/vm/gas_table.go:226-254
	if has(schedule, GasKeyLog) || has(schedule, GasKeyLogTopic) || has(schedule, GasKeyLogData) {
		baseGas := get(schedule, GasKeyLog, params.LogGas)
		topicGas := get(schedule, GasKeyLogTopic, params.LogTopicGas)
		dataGas := get(schedule, GasKeyLogData, params.LogDataGas)
		if jt[vm.LOG0] != nil {
			jt[vm.LOG0].SetDynamicGas(makeCustomLogGas(0, baseGas, topicGas, dataGas))
		}
		if jt[vm.LOG1] != nil {
			jt[vm.LOG1].SetDynamicGas(makeCustomLogGas(1, baseGas, topicGas, dataGas))
		}
		if jt[vm.LOG2] != nil {
			jt[vm.LOG2].SetDynamicGas(makeCustomLogGas(2, baseGas, topicGas, dataGas))
		}
		if jt[vm.LOG3] != nil {
			jt[vm.LOG3].SetDynamicGas(makeCustomLogGas(3, baseGas, topicGas, dataGas))
		}
		if jt[vm.LOG4] != nil {
			jt[vm.LOG4].SetDynamicGas(makeCustomLogGas(4, baseGas, topicGas, dataGas))
		}
	}

	// COPY operations - uses dynamic gas for copy cost per word
	// VERIFIED: Matches memoryCopierGas in execution/vm/gas_table.go:72-94
	if has(schedule, GasKeyCopy) {
		copyGas := get(schedule, GasKeyCopy, params.CopyGas)
		if jt[vm.CALLDATACOPY] != nil {
			jt[vm.CALLDATACOPY].SetDynamicGas(makeCustomCopyGas(2, copyGas))
		}
		if jt[vm.CODECOPY] != nil {
			jt[vm.CODECOPY].SetDynamicGas(makeCustomCopyGas(2, copyGas))
		}
		if jt[vm.RETURNDATACOPY] != nil {
			jt[vm.RETURNDATACOPY].SetDynamicGas(makeCustomCopyGas(2, copyGas))
		}
		if jt[vm.EXTCODECOPY] != nil {
			jt[vm.EXTCODECOPY].SetDynamicGas(makeCustomCopyGas(3, copyGas))
		}
		if jt[vm.MCOPY] != nil {
			jt[vm.MCOPY].SetDynamicGas(makeCustomCopyGas(2, copyGas))
		}
	}

	// CREATE/CREATE2 - base cost only
	if has(schedule, vm.CREATE.String()) {
		if jt[vm.CREATE] != nil {
			jt[vm.CREATE].SetConstantGas(get(schedule, vm.CREATE.String(), params.CreateGas))
		}
	}
	if has(schedule, vm.CREATE2.String()) {
		if jt[vm.CREATE2] != nil {
			jt[vm.CREATE2].SetConstantGas(get(schedule, vm.CREATE2.String(), params.Create2Gas))
		}
	}

	// CALL family - uses dynamic gas for cold/warm + value + new account + memory
	// VERIFIED: Matches makeCallVariantGasCallEIP2929 wrapping gasCall/gasDelegateCall/etc
	// in execution/vm/operations_acl.go:157-191 and gas_table.go:381-524
	if has(schedule, GasKeyCallCold) || has(schedule, GasKeyCallWarm) || has(schedule, GasKeyCallValueXfer) || has(schedule, GasKeyCallNewAccount) {
		callParams := &callGasParams{
			coldAccessCost: get(schedule, GasKeyCallCold, params.ColdAccountAccessCostEIP2929),
			warmAccessCost: get(schedule, GasKeyCallWarm, params.WarmStorageReadCostEIP2929),
			valueXferCost:  get(schedule, GasKeyCallValueXfer, params.CallValueTransferGas),
			newAccountCost: get(schedule, GasKeyCallNewAccount, params.CallNewAccountGas),
		}

		// IMPORTANT: constantGas must be set to warm cost (Erigon pattern)
		// The dynamic gas function then adds (cold - warm) for cold access
		if jt[vm.CALL] != nil {
			jt[vm.CALL].SetConstantGas(callParams.warmAccessCost)
			jt[vm.CALL].SetDynamicGas(makeCustomCallGasEIP2929(callParams, true))
		}
		if jt[vm.CALLCODE] != nil {
			jt[vm.CALLCODE].SetConstantGas(callParams.warmAccessCost)
			jt[vm.CALLCODE].SetDynamicGas(makeCustomCallCodeGasEIP2929(callParams))
		}
		if jt[vm.DELEGATECALL] != nil {
			jt[vm.DELEGATECALL].SetConstantGas(callParams.warmAccessCost)
			jt[vm.DELEGATECALL].SetDynamicGas(makeCustomDelegateCallGasEIP2929(callParams.coldAccessCost, callParams.warmAccessCost))
		}
		if jt[vm.STATICCALL] != nil {
			jt[vm.STATICCALL].SetConstantGas(callParams.warmAccessCost)
			jt[vm.STATICCALL].SetDynamicGas(makeCustomStaticCallGasEIP2929(callParams.coldAccessCost, callParams.warmAccessCost))
		}
	}

	// Balance/ExtCode - constant gas opcodes with cold/warm handled by access list
	if has(schedule, vm.BALANCE.String()) {
		if jt[vm.BALANCE] != nil {
			jt[vm.BALANCE].SetConstantGas(get(schedule, vm.BALANCE.String(), params.BalanceGasEIP1884))
		}
	}
	if has(schedule, vm.EXTCODESIZE.String()) {
		if jt[vm.EXTCODESIZE] != nil {
			jt[vm.EXTCODESIZE].SetConstantGas(get(schedule, vm.EXTCODESIZE.String(), params.ExtcodeSizeGasEIP150))
		}
	}
	if has(schedule, vm.EXTCODECOPY.String()) {
		if jt[vm.EXTCODECOPY] != nil {
			jt[vm.EXTCODECOPY].SetConstantGas(get(schedule, vm.EXTCODECOPY.String(), params.ExtcodeCopyBaseEIP150))
		}
	}
	if has(schedule, vm.EXTCODEHASH.String()) {
		if jt[vm.EXTCODEHASH] != nil {
			jt[vm.EXTCODEHASH].SetConstantGas(get(schedule, vm.EXTCODEHASH.String(), params.ExtcodeHashGasEIP1884))
		}
	}
	if has(schedule, vm.SELFDESTRUCT.String()) {
		if jt[vm.SELFDESTRUCT] != nil {
			jt[vm.SELFDESTRUCT].SetConstantGas(get(schedule, vm.SELFDESTRUCT.String(), params.SelfdestructGasEIP150))
		}
	}
}

// sstoreGasParams holds all the configurable gas parameters for SSTORE.
type sstoreGasParams struct {
	coldSloadCost  uint64
	warmReadCost   uint64
	setGas         uint64
	resetGas       uint64
	clearingRefund uint64
	sentryGas      uint64
}

// calculateClearingRefund calculates the SSTORE clearing refund based on custom gas values.
// Formula: SSTORE_RESET_GAS - COLD_SLOAD_COST + ACCESS_LIST_STORAGE_KEY_GAS
// This matches params.SstoreClearsScheduleRefundEIP3529 calculation.
// Returns 0 if the calculation would underflow (when coldSloadCost > resetGas).
func calculateClearingRefund(resetGas, coldSloadCost uint64) uint64 {
	// Guard against underflow: if coldSloadCost > resetGas, return 0
	if coldSloadCost > resetGas {
		return 0
	}
	return (resetGas - coldSloadCost) + params.TxAccessListStorageKeyGas
}

// makeCustomSloadGas creates a custom SLOAD dynamic gas function with the given cold/warm costs.
//
// VERIFIED against execution/vm/operations_acl.go:106-114 (gasSLoadEIP2929)
// Logic is identical - only difference is parameterized gas values instead of hardcoded params.
func makeCustomSloadGas(coldCost, warmCost uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		loc := callContext.Stack.Back(0)
		// If the caller cannot afford the cost, this change will be rolled back
		// If he does afford it, we can skip checking the same thing later on, during execution
		if _, slotMod := evm.IntraBlockState().AddSlotToAccessList(callContext.Address(), accounts.InternKey(loc.Bytes32())); slotMod {
			return coldCost, nil
		}
		return warmCost, nil
	}
}

// makeCustomSstoreGas creates a custom SSTORE dynamic gas function.
//
// VERIFIED against execution/vm/operations_acl.go:33-99 (makeGasSStoreFunc)
// Logic is identical - only difference is parameterized gas values instead of hardcoded params.
//
// Key formulas that use subtraction (require underflow protection):
// - Line 72 in Erigon: cost + (params.SstoreResetGasEIP2200 - params.ColdSloadCostEIP2929)
// - Line 92 in Erigon: (params.SstoreResetGasEIP2200 - params.ColdSloadCostEIP2929) - params.WarmStorageReadCostEIP2929
//
// With default values these never underflow, but with custom values they can.
// We use safeSub to return 0 instead of underflowing.
func makeCustomSstoreGas(p *sstoreGasParams) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		// If we fail the minimum gas availability invariant, fail (0)
		if scopeGas <= p.sentryGas {
			return 0, errors.New("not enough gas for reentrancy sentry")
		}
		// Gas sentry honoured, do the actual gas calculation based on the stored value
		var (
			y, x    = callContext.Stack.Back(1), callContext.Stack.Back(0)
			slot    = accounts.InternKey(x.Bytes32())
			current uint256.Int
			cost    = uint64(0)
		)

		current, _ = evm.IntraBlockState().GetState(callContext.Address(), slot)
		// If the caller cannot afford the cost, this change will be rolled back
		if _, slotMod := evm.IntraBlockState().AddSlotToAccessList(callContext.Address(), slot); slotMod {
			cost = p.coldSloadCost
		}
		var value uint256.Int
		value.Set(y)

		if current.Eq(&value) { // noop (1)
			// EIP 2200 original clause:
			//		return params.SloadGasEIP2200, nil
			return cost + p.warmReadCost, nil // SLOAD_GAS
		}

		slotCommited := accounts.InternKey(x.Bytes32())
		original, _ := evm.IntraBlockState().GetCommittedState(callContext.Address(), slotCommited)
		if original.Eq(&current) {
			if original.IsZero() { // create slot (2.1.1)
				return cost + p.setGas, nil
			}
			if value.IsZero() { // delete slot (2.1.2b)
				evm.IntraBlockState().AddRefund(p.clearingRefund)
			}
			// EIP-2200 original clause:
			//		return params.SstoreResetGasEIP2200, nil // write existing slot (2.1.2)
			// Formula: cost + (SSTORE_RESET_GAS - COLD_SLOAD_COST)
			// Use safeSub to prevent underflow when coldSloadCost > resetGas
			return cost + safeSub(p.resetGas, p.coldSloadCost), nil // write existing slot (2.1.2)
		}
		if !original.IsZero() {
			if current.IsZero() { // recreate slot (2.2.1.1)
				evm.IntraBlockState().SubRefund(p.clearingRefund)
			} else if value.IsZero() { // delete slot (2.2.1.2)
				evm.IntraBlockState().AddRefund(p.clearingRefund)
			}
		}
		if original.Eq(&value) {
			if original.IsZero() { // reset to original inexistent slot (2.2.2.1)
				// EIP 2200 Original clause:
				//evm.StateDB.AddRefund(params.SstoreSetGasEIP2200 - params.SloadGasEIP2200)
				evm.IntraBlockState().AddRefund(safeSub(p.setGas, p.warmReadCost))
			} else { // reset to original existing slot (2.2.2.2)
				// EIP 2200 Original clause:
				//	evm.StateDB.AddRefund(params.SstoreResetGasEIP2200 - params.SloadGasEIP2200)
				// - SSTORE_RESET_GAS redefined as (5000 - COLD_SLOAD_COST)
				// - SLOAD_GAS redefined as WARM_STORAGE_READ_COST
				// Final: (SSTORE_RESET_GAS - COLD_SLOAD_COST) - WARM_STORAGE_READ_COST
				resetMinusCold := safeSub(p.resetGas, p.coldSloadCost)
				evm.IntraBlockState().AddRefund(safeSub(resetMinusCold, p.warmReadCost))
			}
		}
		// EIP-2200 original clause:
		//return params.SloadGasEIP2200, nil // dirty update (2.2)
		return cost + p.warmReadCost, nil // dirty update (2.2)
	}
}

// safeSub returns a - b, or 0 if a < b (prevents underflow).
func safeSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

// memoryGasCost calculates the quadratic gas for memory expansion.
//
// VERIFIED against execution/vm/gas_table.go:35-62 (memoryGasCost)
// Logic is identical - uses exported methods (LastGasCost/SetLastGasCost) instead of
// direct field access (lastGasCost) since we're in a different package.
func memoryGasCost(callContext *vm.CallContext, newMemSize uint64) (uint64, error) {
	if newMemSize == 0 {
		return 0, nil
	}
	// The maximum that will fit in a uint64 is max_word_count - 1. Anything above
	// that will result in an overflow. Additionally, a newMemSize which results in
	// a newMemSizeWords larger than 0xFFFFFFFF will cause the square operation to
	// overflow. The constant 0x1FFFFFFFE0 is the highest number that can be used
	// without overflowing the gas calculation.
	if newMemSize > 0x1FFFFFFFE0 {
		return 0, vm.ErrGasUintOverflow
	}
	newMemSizeWords := toWordSize(newMemSize)
	newMemSize = newMemSizeWords * 32

	if newMemSize > uint64(callContext.Memory.Len()) {
		square := newMemSizeWords * newMemSizeWords
		linCoef := newMemSizeWords * params.MemoryGas
		quadCoef := square / params.QuadCoeffDiv
		newTotalFee := linCoef + quadCoef

		fee := newTotalFee - callContext.Memory.LastGasCost()
		callContext.Memory.SetLastGasCost(newTotalFee)

		return fee, nil
	}
	return 0, nil
}

// toWordSize returns the number of words required to hold a given number of bytes.
//
// VERIFIED against execution/vm/common.go:ToWordSize (same formula)
func toWordSize(size uint64) uint64 {
	if size > math.MaxUint64-31 {
		return math.MaxUint64/32 + 1
	}
	return (size + 31) / 32
}

// makeCustomExpGas creates a custom EXP dynamic gas function.
//
// VERIFIED against execution/vm/gas_table.go:368-379 (gasExpEIP160)
// Logic is identical - only difference is parameterized gas values.
func makeCustomExpGas(baseGas, byteGas uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(_ *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		expByteLen := uint64(common.BitLenToByteLen(callContext.Stack.Back(1).BitLen()))

		var (
			gas      = expByteLen * byteGas // no overflow check required. Max is 256 * ExpByte gas
			overflow bool
		)
		if gas, overflow = math.SafeAdd(gas, baseGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		return gas, nil
	}
}

// makeCustomKeccak256Gas creates a custom KECCAK256 dynamic gas function.
//
// VERIFIED against execution/vm/gas_table.go:256-272 (gasKeccak256)
// Logic is identical - only difference is parameterized word gas.
func makeCustomKeccak256Gas(wordGas uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(_ *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		gas, err := memoryGasCost(callContext, memorySize)
		if err != nil {
			return 0, err
		}
		wordSize, overflow := callContext.Stack.Back(1).Uint64WithOverflow()
		if overflow {
			return 0, vm.ErrGasUintOverflow
		}
		if wordSize, overflow = math.SafeMul(toWordSize(wordSize), wordGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		if gas, overflow = math.SafeAdd(gas, wordSize); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		return gas, nil
	}
}

// makeCustomLogGas creates a custom LOG dynamic gas function.
//
// VERIFIED against execution/vm/gas_table.go:226-254 (makeGasLog)
// Logic is identical - only difference is parameterized gas values.
func makeCustomLogGas(numTopics uint64, baseGas, topicGas, dataGas uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(_ *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		requestedSize, overflow := callContext.Stack.Back(1).Uint64WithOverflow()
		if overflow {
			return 0, vm.ErrGasUintOverflow
		}

		gas, err := memoryGasCost(callContext, memorySize)
		if err != nil {
			return 0, err
		}

		if gas, overflow = math.SafeAdd(gas, baseGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		if gas, overflow = math.SafeAdd(gas, numTopics*topicGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}

		var memorySizeGas uint64
		if memorySizeGas, overflow = math.SafeMul(requestedSize, dataGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		if gas, overflow = math.SafeAdd(gas, memorySizeGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		return gas, nil
	}
}

// makeCustomCopyGas creates a custom copy gas function for CALLDATACOPY, CODECOPY, etc.
//
// VERIFIED against execution/vm/gas_table.go:72-94 (memoryCopierGas)
// Logic is identical - only difference is parameterized copy gas.
func makeCustomCopyGas(stackpos int, copyGas uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(_ *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		// Gas for expanding the memory
		gas, err := memoryGasCost(callContext, memorySize)
		if err != nil {
			return 0, err
		}
		// And gas for copying data, charged per word at param.CopyGas
		words, overflow := callContext.Stack.Back(stackpos).Uint64WithOverflow()
		if overflow {
			return 0, vm.ErrGasUintOverflow
		}

		if words, overflow = math.SafeMul(toWordSize(words), copyGas); overflow {
			return 0, vm.ErrGasUintOverflow
		}

		if gas, overflow = math.SafeAdd(gas, words); overflow {
			return 0, vm.ErrGasUintOverflow
		}
		return gas, nil
	}
}

// callGasParams holds all the configurable gas parameters for CALL opcodes.
type callGasParams struct {
	coldAccessCost uint64
	warmAccessCost uint64
	valueXferCost  uint64
	newAccountCost uint64
}

// makeCustomCallGasEIP2929 creates a custom CALL dynamic gas function.
//
// VERIFIED against:
// - execution/vm/operations_acl.go:157-191 (makeCallVariantGasCallEIP2929)
// - execution/vm/gas_table.go:381-435 (gasCall)
//
// This follows Erigon's pattern exactly:
// 1. Check cold access, deduct (cold-warm) from scopeGas BEFORE calling inner calculator
// 2. Inner calculator handles value transfer, new account, memory, and 63/64ths rule
// 3. Add cold cost back to return value so it's reported correctly to tracers
func makeCustomCallGasEIP2929(p *callGasParams, hasValue bool) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		addr := accounts.InternAddress(callContext.Stack.Back(1).Bytes20())

		// The WarmStorageReadCostEIP2929 (100) is already deducted in the form of a constant cost, so
		// the cost to charge for cold access, if any, is Cold - Warm
		coldCost := safeSub(p.coldAccessCost, p.warmAccessCost)

		addrMod := evm.IntraBlockState().AddAddressToAccessList(addr)
		warmAccess := !addrMod
		if addrMod {
			// Charge the remaining difference here already, to correctly calculate available
			// gas for call
			if scopeGas < coldCost {
				return 0, vm.ErrOutOfGas
			}
			scopeGas -= coldCost
		}

		// Now call the inner calculator, which takes into account
		// - create new account
		// - transfer value
		// - memory expansion
		// - 63/64ths rule
		gas, err := gasCallInner(evm, callContext, scopeGas, memorySize, p, hasValue)
		if warmAccess || err != nil {
			return gas, err
		}
		// In case of a cold access, we temporarily add the cold charge back, and also
		// add it to the returned gas. By adding it to the return, it will be charged
		// outside of this function, as part of the dynamic gas, and that will make it
		// also become correctly reported to tracers.
		return gas + coldCost, nil
	}
}

// gasCallInner is the inner CALL gas calculator that handles value transfer,
// new account creation, memory expansion, and the 63/64ths rule.
//
// VERIFIED against execution/vm/gas_table.go:381-435 (gasCall)
// Logic is identical except we removed the debug tracing (not relevant for simulation).
func gasCallInner(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64, p *callGasParams, hasValue bool) (uint64, error) {
	var (
		gas            uint64
		transfersValue = hasValue && !callContext.Stack.Back(2).IsZero()
		address        = accounts.InternAddress(callContext.Stack.Back(1).Bytes20())
	)

	if evm.ChainRules().IsSpuriousDragon {
		empty, err := evm.IntraBlockState().Empty(address)
		if err != nil {
			return 0, err
		}
		if transfersValue && empty {
			gas += p.newAccountCost
		}
	} else {
		exists, err := evm.IntraBlockState().Exist(address)
		if err != nil {
			return 0, err
		}
		if !exists {
			gas += p.newAccountCost
		}
	}

	if transfersValue {
		gas += p.valueXferCost
	}

	memoryGas, err := memoryGasCost(callContext, memorySize)
	if err != nil {
		return 0, err
	}

	var overflow bool
	if gas, overflow = math.SafeAdd(gas, memoryGas); overflow {
		return 0, vm.ErrGasUintOverflow
	}

	callGasTemp, err := callGas(evm.ChainRules().IsTangerineWhistle, scopeGas, gas, callContext.Stack.Back(0))
	if err != nil {
		return 0, err
	}
	evm.SetCallGasTemp(callGasTemp)

	if gas, overflow = math.SafeAdd(gas, callGasTemp); overflow {
		return 0, vm.ErrGasUintOverflow
	}

	return gas, nil
}

// makeCustomCallCodeGasEIP2929 creates a custom CALLCODE dynamic gas function.
//
// VERIFIED against:
// - execution/vm/operations_acl.go:157-191 (makeCallVariantGasCallEIP2929)
// - execution/vm/gas_table.go:437-471 (gasCallCode)
func makeCustomCallCodeGasEIP2929(p *callGasParams) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		addr := accounts.InternAddress(callContext.Stack.Back(1).Bytes20())

		coldCost := safeSub(p.coldAccessCost, p.warmAccessCost)

		addrMod := evm.IntraBlockState().AddAddressToAccessList(addr)
		warmAccess := !addrMod
		if addrMod {
			if scopeGas < coldCost {
				return 0, vm.ErrOutOfGas
			}
			scopeGas -= coldCost
		}

		gas, err := gasCallCodeInner(evm, callContext, scopeGas, memorySize, p)
		if warmAccess || err != nil {
			return gas, err
		}
		return gas + coldCost, nil
	}
}

// gasCallCodeInner is the inner CALLCODE gas calculator.
//
// VERIFIED against execution/vm/gas_table.go:437-471 (gasCallCode)
func gasCallCodeInner(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64, p *callGasParams) (uint64, error) {
	memoryGas, err := memoryGasCost(callContext, memorySize)
	if err != nil {
		return 0, err
	}
	var (
		gas      uint64
		overflow bool
	)
	if !callContext.Stack.Back(2).IsZero() {
		gas += p.valueXferCost
	}

	if gas, overflow = math.SafeAdd(gas, memoryGas); overflow {
		return 0, vm.ErrGasUintOverflow
	}

	callGasTemp, err := callGas(evm.ChainRules().IsTangerineWhistle, scopeGas, gas, callContext.Stack.Back(0))
	if err != nil {
		return 0, err
	}
	evm.SetCallGasTemp(callGasTemp)

	if gas, overflow = math.SafeAdd(gas, callGasTemp); overflow {
		return 0, vm.ErrGasUintOverflow
	}
	return gas, nil
}

// makeCustomDelegateCallGasEIP2929 creates a custom DELEGATECALL dynamic gas function.
//
// VERIFIED against:
// - execution/vm/operations_acl.go:157-191 (makeCallVariantGasCallEIP2929)
// - execution/vm/gas_table.go:473-497 (gasDelegateCall)
func makeCustomDelegateCallGasEIP2929(coldAccessCost, warmAccessCost uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		addr := accounts.InternAddress(callContext.Stack.Back(1).Bytes20())

		coldCost := safeSub(coldAccessCost, warmAccessCost)

		addrMod := evm.IntraBlockState().AddAddressToAccessList(addr)
		warmAccess := !addrMod
		if addrMod {
			if scopeGas < coldCost {
				return 0, vm.ErrOutOfGas
			}
			scopeGas -= coldCost
		}

		gas, err := gasDelegateCallInner(evm, callContext, scopeGas, memorySize)
		if warmAccess || err != nil {
			return gas, err
		}
		return gas + coldCost, nil
	}
}

// gasDelegateCallInner is the inner DELEGATECALL gas calculator.
//
// VERIFIED against execution/vm/gas_table.go:473-497 (gasDelegateCall)
func gasDelegateCallInner(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
	gas, err := memoryGasCost(callContext, memorySize)
	if err != nil {
		return 0, err
	}

	callGasTemp, err := callGas(evm.ChainRules().IsTangerineWhistle, scopeGas, gas, callContext.Stack.Back(0))
	if err != nil {
		return 0, err
	}
	evm.SetCallGasTemp(callGasTemp)

	var overflow bool
	if gas, overflow = math.SafeAdd(gas, callGasTemp); overflow {
		return 0, vm.ErrGasUintOverflow
	}
	return gas, nil
}

// makeCustomStaticCallGasEIP2929 creates a custom STATICCALL dynamic gas function.
//
// VERIFIED against:
// - execution/vm/operations_acl.go:157-191 (makeCallVariantGasCallEIP2929)
// - execution/vm/gas_table.go:499-524 (gasStaticCall)
func makeCustomStaticCallGasEIP2929(coldAccessCost, warmAccessCost uint64) func(*vm.EVM, *vm.CallContext, uint64, uint64) (uint64, error) {
	return func(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
		addr := accounts.InternAddress(callContext.Stack.Back(1).Bytes20())

		coldCost := safeSub(coldAccessCost, warmAccessCost)

		addrMod := evm.IntraBlockState().AddAddressToAccessList(addr)
		warmAccess := !addrMod
		if addrMod {
			if scopeGas < coldCost {
				return 0, vm.ErrOutOfGas
			}
			scopeGas -= coldCost
		}

		gas, err := gasStaticCallInner(evm, callContext, scopeGas, memorySize)
		if warmAccess || err != nil {
			return gas, err
		}
		return gas + coldCost, nil
	}
}

// gasStaticCallInner is the inner STATICCALL gas calculator.
//
// VERIFIED against execution/vm/gas_table.go:499-524 (gasStaticCall)
func gasStaticCallInner(evm *vm.EVM, callContext *vm.CallContext, scopeGas uint64, memorySize uint64) (uint64, error) {
	gas, err := memoryGasCost(callContext, memorySize)
	if err != nil {
		return 0, err
	}

	callGasTemp, err := callGas(evm.ChainRules().IsTangerineWhistle, scopeGas, gas, callContext.Stack.Back(0))
	if err != nil {
		return 0, err
	}
	evm.SetCallGasTemp(callGasTemp)

	var overflow bool
	if gas, overflow = math.SafeAdd(gas, callGasTemp); overflow {
		return 0, vm.ErrGasUintOverflow
	}

	return gas, nil
}

// callGas returns the actual gas cost of the call (63/64ths rule from EIP-150).
//
// VERIFIED against execution/vm/gas.go:40-60 (callGas)
// Logic is identical.
func callGas(isEip150 bool, availableGas, base uint64, callCost *uint256.Int) (uint64, error) {
	if isEip150 {
		// Guard against underflow: if availableGas < base, no gas can be passed to the child call
		if availableGas < base {
			return 0, nil
		}
		availableGas = availableGas - base
		gas := availableGas - availableGas/64
		// If the bit length exceeds 64 bit we know that the newly calculated "gas" for EIP150
		// is smaller than the requested amount. Therefore we return the new gas instead
		// of returning an error.
		if !callCost.IsUint64() || gas < callCost.Uint64() {
			return gas, nil
		}
	}
	if !callCost.IsUint64() {
		return 0, vm.ErrGasUintOverflow
	}

	return callCost.Uint64(), nil
}

// opcodeFromString returns the opcode for a given string name.
func opcodeFromString(name string) (vm.OpCode, bool) {
	op, ok := opcodeMap[name]
	return op, ok
}

// opcodeMap maps opcode string names to OpCode values.
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
	"CLZ":            vm.CLZ,
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
	"DUPN":           vm.DUPN,
	"SWAPN":          vm.SWAPN,
	"EXCHANGE":       vm.EXCHANGE,
}
