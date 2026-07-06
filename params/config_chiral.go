// Copyright 2026 The core-geth Authors
// This file is part of the core-geth library.
//
// The core-geth library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The core-geth library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the core-geth library. If not, see <http://www.gnu.org/licenses/>.
package params

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
)

var (
	// ChiralChainConfig is the chain parameters to run a node on the Chiral
	// main network: RandomX v2 proof-of-work with the modern EVM feature set
	// enabled from genesis.
	//
	// All EIPs through the Shanghai EVM set activate at block 0, with the
	// beacon-chain-coupled ones deliberately excluded: EIP-4895 withdrawals,
	// EIP-4844/4788 (Cancun blobs/beacon root) and EIP-4399 (PREVRANDAO —
	// the DIFFICULTY opcode stays meaningful on a PoW chain).
	//
	// TODO(chiral): NetworkID/ChainID are placeholders pending a launch
	// decision (see PLAN.md).
	ChiralChainConfig = &coregeth.CoreGethChainConfig{
		NetworkID: 61803,
		ChainID:   big.NewInt(61803),
		RandomX:   new(ctypes.RandomXConfig),

		// Homestead eq
		EIP2FBlock: big.NewInt(0),
		EIP7FBlock: big.NewInt(0),

		// Tangerine Whistle
		EIP150Block: big.NewInt(0),

		// Spurious Dragon eq
		EIP155Block:  big.NewInt(0),
		EIP160FBlock: big.NewInt(0),
		EIP161FBlock: big.NewInt(0),
		EIP170FBlock: big.NewInt(0),

		// Byzantium eq
		EIP100FBlock: big.NewInt(0),
		EIP140FBlock: big.NewInt(0),
		EIP198FBlock: big.NewInt(0),
		EIP211FBlock: big.NewInt(0),
		EIP212FBlock: big.NewInt(0),
		EIP213FBlock: big.NewInt(0),
		EIP214FBlock: big.NewInt(0),
		EIP658FBlock: big.NewInt(0),

		// Constantinople/Petersburg eq
		EIP145FBlock:  big.NewInt(0),
		EIP1014FBlock: big.NewInt(0),
		EIP1052FBlock: big.NewInt(0),

		// Istanbul eq
		EIP152FBlock:  big.NewInt(0),
		EIP1108FBlock: big.NewInt(0),
		EIP1344FBlock: big.NewInt(0),
		EIP1884FBlock: big.NewInt(0),
		EIP2028FBlock: big.NewInt(0),
		EIP2200FBlock: big.NewInt(0),

		// Berlin eq
		EIP2565FBlock: big.NewInt(0),
		EIP2718FBlock: big.NewInt(0),
		EIP2929FBlock: big.NewInt(0),
		EIP2930FBlock: big.NewInt(0),

		// London eq (with fee market; no difficulty-bomb EIPs, the bomb is
		// disposed below)
		EIP1559FBlock: big.NewInt(0),
		EIP3198FBlock: big.NewInt(0),
		EIP3529FBlock: big.NewInt(0),
		EIP3541FBlock: big.NewInt(0),

		// Shanghai EVM eq (without EIP-4895 withdrawals)
		EIP3651FBlock: big.NewInt(0),
		EIP3855FBlock: big.NewInt(0),
		EIP3860FBlock: big.NewInt(0),

		DisposalBlock: big.NewInt(0), // no difficulty bomb, ever

		RequireBlockHashes: map[uint64]common.Hash{},
	}
)
