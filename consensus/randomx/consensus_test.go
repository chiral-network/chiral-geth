package randomx

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/goethereum"
)

// testChain is a minimal in-memory consensus.ChainHeaderReader.
type testChain struct {
	conf   ctypes.ChainConfigurator
	mu     sync.Mutex
	canon  []*types.Header
	byHash map[common.Hash]*types.Header
}

func newTestChain(genesis *types.Header) *testChain {
	c := &testChain{
		conf:   &goethereum.ChainConfig{ChainID: big.NewInt(1337)},
		byHash: make(map[common.Hash]*types.Header),
	}
	c.insert(genesis)
	return c
}

func (c *testChain) insert(h *types.Header) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h.Number.Uint64() != uint64(len(c.canon)) {
		panic("non-contiguous test chain insert")
	}
	c.canon = append(c.canon, h)
	c.byHash[h.Hash()] = h
}

func (c *testChain) Config() ctypes.ChainConfigurator { return c.conf }

func (c *testChain) CurrentHeader() *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.canon[len(c.canon)-1]
}

func (c *testChain) GetHeader(hash common.Hash, number uint64) *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h := c.byHash[hash]; h != nil && h.Number.Uint64() == number {
		return h
	}
	return nil
}

func (c *testChain) GetHeaderByNumber(number uint64) *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if number < uint64(len(c.canon)) {
		return c.canon[number]
	}
	return nil
}

func (c *testChain) GetHeaderByHash(hash common.Hash) *types.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byHash[hash]
}

func (c *testChain) GetTd(hash common.Hash, number uint64) *big.Int { return nil }

func testGenesis() *types.Header {
	return &types.Header{
		Number:     big.NewInt(0),
		Difficulty: new(big.Int).Set(MinimumDifficulty),
		Time:       0,
		GasLimit:   8_000_000,
		UncleHash:  types.EmptyUncleHash,
		TxHash:     types.EmptyTxsHash,
	}
}

// childHeader builds an unsealed child of parent, prepared by the engine
// (difficulty + seed commitment).
func childHeader(t *testing.T, engine *RandomX, chain *testChain, parent *types.Header) *types.Header {
	t.Helper()
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     new(big.Int).Add(parent.Number, big.NewInt(1)),
		Time:       parent.Time + 13,
		GasLimit:   parent.GasLimit,
		UncleHash:  types.EmptyUncleHash,
		TxHash:     types.EmptyTxsHash,
	}
	if err := engine.Prepare(chain, header); err != nil {
		t.Fatalf("prepare block %d: %v", header.Number, err)
	}
	return header
}

// seal mines the header and returns the sealed version.
func seal(t *testing.T, engine *RandomX, chain *testChain, header *types.Header) *types.Header {
	t.Helper()
	results := make(chan *types.Block, 1)
	stop := make(chan struct{})
	if err := engine.Seal(chain, types.NewBlockWithHeader(header), results, stop); err != nil {
		t.Fatalf("seal block %d: %v", header.Number, err)
	}
	select {
	case block := <-results:
		return block.Header()
	case <-time.After(5 * time.Minute):
		t.Fatalf("sealing block %d timed out", header.Number)
		return nil
	}
}

// newTestEngine creates an engine with tiny seed epochs so tests cross epoch
// boundaries cheaply.
func newTestEngine(t *testing.T) *RandomX {
	t.Helper()
	engine := New(&Config{EpochLength: 8, EpochLag: 2}, nil, false)
	t.Cleanup(func() { engine.Close() })
	return engine
}

func TestSealAndVerifyAcrossEpochs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RandomX mining test in short mode")
	}
	engine := newTestEngine(t)
	genesis := testGenesis()
	chain := newTestChain(genesis)

	// Mine through two epoch switches (seed changes at blocks 11 and 19 with
	// EpochLength=8, EpochLag=2: blocks 1-10 keyed by genesis, 11-18 by
	// block 8, 19+ by block 16). Reaching a third seed also exercises the
	// mining-dataset LRU eviction (maxDatasets=2).
	var headers []*types.Header
	parent := genesis
	for i := 1; i <= 20; i++ {
		header := childHeader(t, engine, chain, parent)

		wantSeed := genesis.Hash()
		switch {
		case header.Number.Uint64() > 18:
			wantSeed = chain.GetHeaderByNumber(16).Hash()
		case header.Number.Uint64() > 10:
			wantSeed = chain.GetHeaderByNumber(8).Hash()
		}
		if header.MixDigest != wantSeed {
			t.Fatalf("block %d: seed commitment %x, want %x", i, header.MixDigest, wantSeed)
		}

		sealed := seal(t, engine, chain, header)
		// Individual verification, including the PoW.
		if err := engine.VerifyHeader(chain, sealed, true); err != nil {
			t.Fatalf("verify block %d: %v", i, err)
		}
		chain.insert(sealed)
		headers = append(headers, sealed)
		parent = sealed
	}

	// Batch verification against a fresh chain that only has the genesis:
	// seed blocks must resolve from within the batch (batchReader overlay).
	fresh := newTestChain(genesis)
	seals := make([]bool, len(headers))
	for i := range seals {
		seals[i] = true
	}
	abort, results := engine.VerifyHeaders(fresh, headers, seals)
	defer close(abort)
	for i := range headers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("batch verify block %d: %v", i+1, err)
			}
		case <-time.After(5 * time.Minute):
			t.Fatalf("batch verification timed out at block %d", i+1)
		}
	}
}

func TestVerifySealErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RandomX mining test in short mode")
	}
	engine := newTestEngine(t)
	genesis := testGenesis()
	chain := newTestChain(genesis)

	header := childHeader(t, engine, chain, genesis)
	sealed := seal(t, engine, chain, header)

	// Corrupt nonce must fail the PoW check. (A single hash collision retry
	// is statistically impossible at test difficulty within one nonce.)
	bad := types.CopyHeader(sealed)
	bad.Nonce = types.EncodeNonce(sealed.Nonce.Uint64() + 1)
	if err := engine.verifySeal(chain, bad, genesis); err != errInvalidPoW {
		t.Errorf("corrupt nonce: got %v, want %v", err, errInvalidPoW)
	}

	// Wrong seed commitment must be rejected before hashing.
	bad = types.CopyHeader(sealed)
	bad.MixDigest = common.HexToHash("0xdeadbeef")
	if err := engine.verifySeal(chain, bad, genesis); err != errInvalidMixDigest {
		t.Errorf("wrong mix digest: got %v, want %v", err, errInvalidMixDigest)
	}

	// Zero difficulty is invalid regardless of the seal.
	bad = types.CopyHeader(sealed)
	bad.Difficulty = new(big.Int)
	if err := engine.verifySeal(chain, bad, genesis); err != errInvalidDifficulty {
		t.Errorf("zero difficulty: got %v, want %v", err, errInvalidDifficulty)
	}
}

func TestSeedBlockNumber(t *testing.T) {
	engine := New(&Config{EpochLength: 2048, EpochLag: 64}, nil, false)
	defer engine.Close()

	for _, tt := range []struct{ number, seed uint64 }{
		{0, 0}, {1, 0}, {2048, 0}, {2112, 0},
		{2113, 2048}, {4160, 2048},
		{4161, 4096}, {4200, 4096},
		{1_000_000, 999_424},
	} {
		if got := engine.seedBlockNumber(tt.number); got != tt.seed {
			t.Errorf("seedBlockNumber(%d): got %d, want %d", tt.number, got, tt.seed)
		}
	}
}

func TestCalcDifficultyClamps(t *testing.T) {
	conf := &goethereum.ChainConfig{ChainID: big.NewInt(1337)}
	parent := testGenesis()

	// Fast block: difficulty rises.
	if d := CalcDifficulty(conf, parent.Time+1, parent); d.Cmp(parent.Difficulty) <= 0 {
		t.Errorf("fast block: difficulty %v did not rise from %v", d, parent.Difficulty)
	}
	// Slow block: clamped at the minimum.
	if d := CalcDifficulty(conf, parent.Time+10_000, parent); d.Cmp(MinimumDifficulty) != 0 {
		t.Errorf("slow block: got %v, want minimum %v", d, MinimumDifficulty)
	}
	// An uncled parent weighs the adjustment one step higher (EIP-100): at
	// the same timestamp delta, difficulty must come out strictly higher.
	bigParent := testGenesis()
	bigParent.Difficulty = big.NewInt(1_000_000) // far from the clamp
	noUncles := CalcDifficulty(conf, bigParent.Time+13, bigParent)
	bigParent.UncleHash = common.HexToHash("0x01") // != EmptyUncleHash
	withUncles := CalcDifficulty(conf, bigParent.Time+13, bigParent)
	if withUncles.Cmp(noUncles) <= 0 {
		t.Errorf("uncled parent: got %v, want > %v", withUncles, noUncles)
	}
}
