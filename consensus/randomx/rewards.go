package randomx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/holiman/uint256"
)

// Chiral monetary policy (decided 2026-07-06; docs/chiral/launch-parameters.md):
// a single geometric decay, chosen once and never edited (the Webchain lesson
// — its three ad-hoc reward changes are permanent code warts in this repo's
// lineage):
//
//	reward(era) = InitialBlockReward × (249/250)^era
//	era         = (blockNumber − 1) / RewardEraLength
//
// Base emission converges to InitialBlockReward × RewardEraLength × 250
// = 1,250,000,000 CHI; half of that is emitted within ~173 eras ≈ 7.1 years
// at 13 s blocks. Uncle inclusion pays up to 2 × 1/32 per block on top of
// the base subsidy (a few percent in practice), so the cap is asymptotic
// for base emission, not a hard total-supply constant.
var (
	// InitialBlockReward is the era-0 block subsidy: 50 CHI.
	InitialBlockReward = new(big.Int).Mul(big.NewInt(50), big.NewInt(1e18))
	// DisinflationRateQuotient/Divisor scale the subsidy by 249/250 each era.
	DisinflationRateQuotient = big.NewInt(249)
	DisinflationRateDivisor  = big.NewInt(250)
	// RewardEraLength is the number of blocks per reward era.
	RewardEraLength = big.NewInt(100_000)
)

// blockEra returns the zero-indexed reward era of the given block number;
// era 0 spans blocks 1..RewardEraLength. Genesis (block 0) pays no reward.
func blockEra(blockNum, eraLength *big.Int) *big.Int {
	if blockNum.Sign() < 1 {
		return new(big.Int)
	}
	return new(big.Int).Div(new(big.Int).Sub(blockNum, big1), eraLength)
}

// blockWinnerReward returns the base miner subsidy for a block in the given
// era: InitialBlockReward × (249/250)^era.
func blockWinnerReward(era *big.Int) *big.Int {
	q := new(big.Int).Exp(DisinflationRateQuotient, era, nil)
	d := new(big.Int).Exp(DisinflationRateDivisor, era, nil)
	r := new(big.Int).Mul(InitialBlockReward, q)
	return r.Div(r, d)
}

// accumulateRewards credits the coinbase of the given block with the era
// subsidy plus 1/32 of it per included uncle; each uncle's coinbase is
// credited 1/32 of the subsidy (MintMe/Webchain-style flat uncle rewards).
func accumulateRewards(config ctypes.ChainConfigurator, state *state.StateDB, header *types.Header, uncles []*types.Header) {
	era := blockEra(header.Number, RewardEraLength)
	reward := blockWinnerReward(era)

	uncleReward := new(big.Int).Div(reward, big32)
	total := new(big.Int).Set(reward)
	for _, uncle := range uncles {
		state.AddBalance(uncle.Coinbase, uint256.MustFromBig(uncleReward))
		total.Add(total, uncleReward)
	}
	state.AddBalance(header.Coinbase, uint256.MustFromBig(total))
}
