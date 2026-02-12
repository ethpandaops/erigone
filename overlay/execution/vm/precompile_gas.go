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

import (
	"encoding/binary"

	"github.com/erigontech/erigon/execution/protocol/params"
)

// Precompile gas override key constants.
// Fixed-gas precompiles use PC_<name> as a single total key.
// Variable-gas precompiles use PC_<name>_<param> for each formula parameter.
const (
	GasKeyPCEcrec              = "PC_ECREC"
	GasKeyPCBn254Add           = "PC_BN254_ADD"
	GasKeyPCBn254Mul           = "PC_BN254_MUL"
	GasKeyPCBls12G1Add         = "PC_BLS12_G1ADD"
	GasKeyPCBls12G2Add         = "PC_BLS12_G2ADD"
	GasKeyPCBls12MapFpToG1     = "PC_BLS12_MAP_FP_TO_G1"
	GasKeyPCBls12MapFp2ToG2    = "PC_BLS12_MAP_FP2_TO_G2"
	GasKeyPCKzgPointEvaluation = "PC_KZG_POINT_EVALUATION"
	GasKeyPCP256Verify         = "PC_P256VERIFY"

	GasKeyPCSha256Base    = "PC_SHA256_BASE"
	GasKeyPCSha256PerWord = "PC_SHA256_PER_WORD"

	GasKeyPCRipemd160Base    = "PC_RIPEMD160_BASE"
	GasKeyPCRipemd160PerWord = "PC_RIPEMD160_PER_WORD"

	GasKeyPCIdBase    = "PC_ID_BASE"
	GasKeyPCIdPerWord = "PC_ID_PER_WORD"

	GasKeyPCModexpMinGas = "PC_MODEXP_MIN_GAS"

	GasKeyPCBn254PairingBase    = "PC_BN254_PAIRING_BASE"
	GasKeyPCBn254PairingPerPair = "PC_BN254_PAIRING_PER_PAIR"

	GasKeyPCBlake2fBase     = "PC_BLAKE2F_BASE"
	GasKeyPCBlake2fPerRound = "PC_BLAKE2F_PER_ROUND"

	GasKeyPCBls12PairingBase    = "PC_BLS12_PAIRING_CHECK_BASE"
	GasKeyPCBls12PairingPerPair = "PC_BLS12_PAIRING_CHECK_PER_PAIR"

	GasKeyPCBls12G1MsmMulGas = "PC_BLS12_G1MSM_MUL_GAS"
	GasKeyPCBls12G2MsmMulGas = "PC_BLS12_G2MSM_MUL_GAS"
)

// PrecompileGasWithOverrides calculates precompile gas cost with optional overrides.
// Fixed-gas precompiles: single key (PC_<name>) overrides the flat cost.
// Variable-gas precompiles: parameter keys (PC_<name>_BASE, etc.) override formula inputs.
func PrecompileGasWithOverrides(schedule *GasSchedule, name string, input []byte, defaultGas uint64) uint64 {
	if schedule == nil {
		return defaultGas
	}

	switch name {
	// Fixed-gas precompiles — single total key
	case "ECREC", "BN254_ADD", "BN254_MUL", "BLS12_G1ADD", "BLS12_G2ADD",
		"BLS12_MAP_FP_TO_G1", "BLS12_MAP_FP2_TO_G2", "KZG_POINT_EVALUATION", "P256VERIFY":
		return schedule.GetOr("PC_"+name, defaultGas)

	// Variable-gas precompiles — parameter overrides
	case "SHA256":
		return precompileBasePerWord(schedule, GasKeyPCSha256Base, GasKeyPCSha256PerWord, input, params.Sha256BaseGas, params.Sha256PerWordGas)
	case "RIPEMD160":
		return precompileBasePerWord(schedule, GasKeyPCRipemd160Base, GasKeyPCRipemd160PerWord, input, params.Ripemd160BaseGas, params.Ripemd160PerWordGas)
	case "ID":
		return precompileBasePerWord(schedule, GasKeyPCIdBase, GasKeyPCIdPerWord, input, params.IdentityBaseGas, params.IdentityPerWordGas)
	case "MODEXP":
		return precompileModexp(schedule, defaultGas)
	case "BN254_PAIRING":
		return precompileBasePerPair(schedule, GasKeyPCBn254PairingBase, GasKeyPCBn254PairingPerPair, input, 192, params.Bn254PairingBaseGasIstanbul, params.Bn254PairingPerPointGasIstanbul)
	case "BLAKE2F":
		return precompileBlake2f(schedule, input)
	case "BLS12_PAIRING_CHECK":
		return precompileBasePerPair(schedule, GasKeyPCBls12PairingBase, GasKeyPCBls12PairingPerPair, input, 384, params.Bls12381PairingBaseGas, params.Bls12381PairingPerPairGas)
	case "BLS12_G1MSM":
		return precompileMsm(schedule, GasKeyPCBls12G1MsmMulGas, input, 160, params.Bls12381G1MulGas)
	case "BLS12_G2MSM":
		return precompileMsm(schedule, GasKeyPCBls12G2MsmMulGas, input, 288, params.Bls12381G2MulGas)
	}

	return defaultGas
}

// precompileBasePerWord computes base + perWord * ceil(len(input)/32).
// Used by SHA256, RIPEMD160, IDENTITY.
func precompileBasePerWord(schedule *GasSchedule, baseKey, perWordKey string, input []byte, defaultBase, defaultPerWord uint64) uint64 {
	base := schedule.GetOr(baseKey, defaultBase)
	perWord := schedule.GetOr(perWordKey, defaultPerWord)
	words := uint64(len(input)+31) / 32
	return base + perWord*words
}

// precompileBasePerPair computes base + perPair * (len(input) / pairSize).
// Used by BN254_PAIRING (pairSize=192), BLS12_PAIRING_CHECK (pairSize=384).
func precompileBasePerPair(schedule *GasSchedule, baseKey, perPairKey string, input []byte, pairSize int, defaultBase, defaultPerPair uint64) uint64 {
	base := schedule.GetOr(baseKey, defaultBase)
	perPair := schedule.GetOr(perPairKey, defaultPerPair)
	pairs := uint64(len(input) / pairSize)
	return base + perPair*pairs
}

// precompileBlake2f computes base + perRound * rounds, where rounds is read from input[0:4].
func precompileBlake2f(schedule *GasSchedule, input []byte) uint64 {
	if len(input) != 213 {
		return 0
	}
	rounds := uint64(binary.BigEndian.Uint32(input[0:4]))
	base := schedule.GetOr(GasKeyPCBlake2fBase, 0)
	perRound := schedule.GetOr(GasKeyPCBlake2fPerRound, 1)
	return base + perRound*rounds
}

// precompileMsm computes k * mulGas * discount[k] / 1000.
// The discount table is not overridable — only the per-point mulGas is.
func precompileMsm(schedule *GasSchedule, mulGasKey string, input []byte, pointSize int, defaultMulGas uint64) uint64 {
	k := len(input) / pointSize
	if k == 0 {
		return 0
	}
	mulGas := schedule.GetOr(mulGasKey, defaultMulGas)

	// Use the correct discount table based on point size
	var discount uint64
	if pointSize == 160 {
		if dLen := len(params.Bls12381MSMDiscountTableG1); k < dLen {
			discount = params.Bls12381MSMDiscountTableG1[k-1]
		} else {
			discount = params.Bls12381MSMDiscountTableG1[dLen-1]
		}
	} else {
		if dLen := len(params.Bls12381MSMDiscountTableG2); k < dLen {
			discount = params.Bls12381MSMDiscountTableG2[k-1]
		} else {
			discount = params.Bls12381MSMDiscountTableG2[dLen-1]
		}
	}

	return (uint64(k) * mulGas * discount) / 1000
}

// precompileModexp applies the MODEXP min gas override.
// The complex EIP-2565/7883 formula itself is not overridable — only the floor value is.
func precompileModexp(schedule *GasSchedule, defaultGas uint64) uint64 {
	minGas := schedule.GetOr(GasKeyPCModexpMinGas, 200)
	if defaultGas < minGas {
		return minGas
	}
	return defaultGas
}
