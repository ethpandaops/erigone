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

package vm

// GasSchedule holds configurable gas costs for simulation.
// When set on the EVM, gas functions use GetOr() to read overridden values
// instead of hardcoded params.X constants.
type GasSchedule struct {
	Overrides map[string]uint64
}

// GetOr returns the override value if set, otherwise the default.
// This allows gas functions to use custom values for simulation while
// falling back to standard values when no override is present.
func (g *GasSchedule) GetOr(key string, defaultVal uint64) uint64 {
	if g != nil && g.Overrides != nil {
		if val, ok := g.Overrides[key]; ok {
			return val
		}
	}

	return defaultVal
}

// Gas parameter keys used by patched gas functions.
// These keys are used in GasSchedule.Overrides to specify custom gas costs.
const (
	GasKeySloadCold            = "SLOAD_COLD"
	GasKeySloadWarm            = "SLOAD_WARM"
	GasKeySstoreSet            = "SSTORE_SET"
	GasKeySstoreReset          = "SSTORE_RESET"
	GasKeyCallCold             = "CALL_COLD"
	GasKeyCallWarm             = "CALL_WARM"
	GasKeyCallValueXfer        = "CALL_VALUE_XFER"
	GasKeyCallNewAccount       = "CALL_NEW_ACCOUNT"
	GasKeyKeccak256Word        = "KECCAK256_WORD"
	GasKeyMemory               = "MEMORY"
	GasKeyCopy                 = "COPY"
	GasKeyLog                  = "LOG"
	GasKeyLogTopic             = "LOG_TOPIC"
	GasKeyLogData              = "LOG_DATA"
	GasKeyExpByte              = "EXP_BYTE"
	GasKeyCreateBySelfDestruct = "CREATE_BY_SELFDESTRUCT"
	GasKeyInitCodeWord         = "INIT_CODE_WORD"
)
