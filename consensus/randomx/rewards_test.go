package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

func TestBlockEraBoundaries(t *testing.T) {
	for _, tt := range []struct{ block, era int64 }{
		{0, 0}, {1, 0}, {100_000, 0},
		{100_001, 1}, {200_000, 1}, {200_001, 2},
		{17_300_000, 172}, {17_300_001, 173},
	} {
		if got := blockEra(big.NewInt(tt.block), RewardEraLength); got.Int64() != tt.era {
			t.Errorf("blockEra(%d): got %d, want %d", tt.block, got, tt.era)
		}
	}
}

func TestBlockWinnerRewardSchedule(t *testing.T) {
	// Era 0: exactly 50 CHI.
	if got, want := blockWinnerReward(big.NewInt(0)), InitialBlockReward; got.Cmp(want) != 0 {
		t.Errorf("era 0: got %v, want %v", got, want)
	}
	// Era 1: exactly 49.8 CHI (50e18 × 249/250 is integer-exact).
	want, _ := new(big.Int).SetString("49800000000000000000", 10)
	if got := blockWinnerReward(big.NewInt(1)); got.Cmp(want) != 0 {
		t.Errorf("era 1: got %v, want %v", got, want)
	}
	// The half-way point of the emission cap falls between eras 172 and 173.
	half := new(big.Int).Div(InitialBlockReward, big.NewInt(2))
	if got := blockWinnerReward(big.NewInt(172)); got.Cmp(half) <= 0 {
		t.Errorf("era 172: got %v, want > %v", got, half)
	}
	if got := blockWinnerReward(big.NewInt(173)); got.Cmp(half) >= 0 {
		t.Errorf("era 173: got %v, want < %v", got, half)
	}
	// Strict monotonic decay over the first few eras.
	prev := blockWinnerReward(big.NewInt(0))
	for era := int64(1); era <= 5; era++ {
		cur := blockWinnerReward(big.NewInt(era))
		if cur.Cmp(prev) >= 0 {
			t.Errorf("era %d: reward %v did not decay from %v", era, cur, prev)
		}
		prev = cur
	}
}

// TestChiralConfigSchedule pins the decided chain parameters: the engine must
// pick up Chiral's seed epoch schedule from the chain config, and the chain
// identity must match the registered values.
func TestChiralConfigSchedule(t *testing.T) {
	cfg := ConfigForChain(params.ChiralChainConfig)
	if cfg.EpochLength != 16384 {
		t.Errorf("seed epoch length: got %d, want 16384", cfg.EpochLength)
	}
	if cfg.EpochLag != 256 {
		t.Errorf("seed epoch lag: got %d, want 256", cfg.EpochLag)
	}
	if id := params.ChiralChainConfig.GetChainID(); id.Cmp(big.NewInt(618033)) != 0 {
		t.Errorf("chain id: got %v, want 618033", id)
	}
	if nid := params.ChiralChainConfig.GetNetworkID(); nid == nil || *nid != 618033 {
		t.Errorf("network id: got %v, want 618033", nid)
	}
}
