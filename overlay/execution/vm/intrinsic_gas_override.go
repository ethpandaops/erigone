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

package vm

import (
	"github.com/erigontech/erigon/common/math"
	"github.com/erigontech/erigon/execution/protocol/params"
)

// Intrinsic gas override keys.
const (
	GasKeyTxBase           = "TX_BASE"
	GasKeyTxCreateBase     = "TX_CREATE_BASE"
	GasKeyTxDataZero       = "TX_DATA_ZERO"
	GasKeyTxDataNonZero    = "TX_DATA_NONZERO"
	GasKeyTxAccessListAddr = "TX_ACCESS_LIST_ADDR"
	GasKeyTxAccessListKey  = "TX_ACCESS_LIST_KEY"
	GasKeyTxInitCodeWord   = "TX_INIT_CODE_WORD"
	GasKeyTxFloorPerToken  = "TX_FLOOR_PER_TOKEN"
	GasKeyTxAuthCost       = "TX_AUTH_COST"
)

// HasIntrinsicOverrides returns true if any intrinsic gas keys are overridden.
func (g *GasSchedule) HasIntrinsicOverrides() bool {
	if g == nil || g.Overrides == nil {
		return false
	}

	for _, key := range []string{
		GasKeyTxBase, GasKeyTxCreateBase, GasKeyTxDataZero, GasKeyTxDataNonZero,
		GasKeyTxAccessListAddr, GasKeyTxAccessListKey, GasKeyTxInitCodeWord,
		GasKeyTxFloorPerToken, GasKeyTxAuthCost,
	} {
		if _, ok := g.Overrides[key]; ok {
			return true
		}
	}

	return false
}

// intrinsicToWordSize returns the ceiled word size required for init code.
// Copied from fixedgas.toWordSize to match upstream overflow guard.
func intrinsicToWordSize(size uint64) uint64 {
	if size > math.MaxUint64-31 {
		return math.MaxUint64/32 + 1
	}

	return (size + 31) / 32
}

// CalcCustomIntrinsicGas recalculates intrinsic gas using GasSchedule overrides.
//
// Mirrors fixedgas.CalcIntrinsicGas logic line-for-line. We duplicate rather
// than patch the original because patching would require changing the function
// signature and updating all callers (5+ files) on every rebase. The intrinsic
// gas formula is very stable (~5 additive changes in 10 years), so drift risk
// is low. If upstream adds new intrinsic gas components, update this function
// to match.
//
// Only called when HasIntrinsicOverrides() is true.
func CalcCustomIntrinsicGas(
	schedule *GasSchedule,
	data []byte,
	accessListLen, storageKeysLen uint64,
	isContractCreation bool,
	isEIP2, isEIP2028, isEIP3860, isEIP7623, isAATxn bool,
	authorizationsLen uint64,
) (gas uint64, floorGas7623 uint64) {
	// Set the starting gas for the raw transaction
	if isContractCreation && isEIP2 {
		gas = schedule.GetOr(GasKeyTxCreateBase, params.TxGasContractCreation)
	} else if isAATxn {
		gas = params.TxAAGas // AA base cost not overridable (niche, can add later)
	} else {
		gas = schedule.GetOr(GasKeyTxBase, params.TxGas)
	}

	floorGas7623 = schedule.GetOr(GasKeyTxBase, params.TxGas)

	// Bump the required gas by the amount of transactional data
	dataLen := uint64(len(data))
	if dataLen > 0 {
		// Zero and non-zero bytes are priced differently
		var nz uint64
		for _, b := range data {
			if b != 0 {
				nz++
			}
		}

		// Make sure we don't exceed uint64 for all data combinations
		nonZeroGas := schedule.GetOr(GasKeyTxDataNonZero, params.TxDataNonZeroGasFrontier)
		if isEIP2028 {
			nonZeroGas = schedule.GetOr(GasKeyTxDataNonZero, params.TxDataNonZeroGasEIP2028)
		}

		product, overflow := math.SafeMul(nz, nonZeroGas)
		if overflow {
			return 0, 0
		}

		gas, overflow = math.SafeAdd(gas, product)
		if overflow {
			return 0, 0
		}

		z := dataLen - nz

		product, overflow = math.SafeMul(z, schedule.GetOr(GasKeyTxDataZero, params.TxDataZeroGas))
		if overflow {
			return 0, 0
		}

		gas, overflow = math.SafeAdd(gas, product)
		if overflow {
			return 0, 0
		}

		if isContractCreation && isEIP3860 {
			numWords := intrinsicToWordSize(dataLen)

			product, overflow = math.SafeMul(numWords, schedule.GetOr(GasKeyTxInitCodeWord, params.InitCodeWordGas))
			if overflow {
				return 0, 0
			}

			gas, overflow = math.SafeAdd(gas, product)
			if overflow {
				return 0, 0
			}
		}

		if isEIP7623 {
			tokenLen := dataLen + 3*nz

			dataGas, overflow := math.SafeMul(tokenLen, schedule.GetOr(GasKeyTxFloorPerToken, params.TxTotalCostFloorPerToken))
			if overflow {
				return 0, 0
			}

			floorGas7623, overflow = math.SafeAdd(floorGas7623, dataGas)
			if overflow {
				return 0, 0
			}
		}
	}

	if accessListLen > 0 {
		product, overflow := math.SafeMul(accessListLen, schedule.GetOr(GasKeyTxAccessListAddr, params.TxAccessListAddressGas))
		if overflow {
			return 0, 0
		}

		gas, overflow = math.SafeAdd(gas, product)
		if overflow {
			return 0, 0
		}

		product, overflow = math.SafeMul(storageKeysLen, schedule.GetOr(GasKeyTxAccessListKey, params.TxAccessListStorageKeyGas))
		if overflow {
			return 0, 0
		}

		gas, overflow = math.SafeAdd(gas, product)
		if overflow {
			return 0, 0
		}
	}

	// Add the cost of authorizations
	product, overflow := math.SafeMul(authorizationsLen, schedule.GetOr(GasKeyTxAuthCost, params.PerEmptyAccountCost))
	if overflow {
		return 0, 0
	}

	gas, overflow = math.SafeAdd(gas, product)
	if overflow {
		return 0, 0
	}

	return gas, floorGas7623
}
