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
	"github.com/ethereum/go-ethereum/params/types/genesisT"
)

// ChiralGenesisHash is the block hash of the Chiral genesis block.
// The constant is checked (and, after genesis changes, regenerated) by
// TestChiralGenesisHash in core/genesis_test.go, which is where genesis
// blocks can be assembled without an import cycle.
var ChiralGenesisHash = common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000")

// DefaultChiralGenesisBlock returns the Chiral network genesis block.
//
// TODO(chiral): timestamp, extraData, initial difficulty, gas limit and
// allocation are placeholders pending launch decisions (see PLAN.md).
func DefaultChiralGenesisBlock() *genesisT.Genesis {
	return &genesisT.Genesis{
		Config:     ChiralChainConfig,
		Nonce:      0,
		Timestamp:  0,
		ExtraData:  []byte("chiral-genesis-v0"),
		GasLimit:   8_000_000,
		Difficulty: big.NewInt(0x20000),
		Alloc:      genesisT.GenesisAlloc{},
	}
}
