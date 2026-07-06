package randomx

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/randomx"
)

// TestRemoteSealer drives the external-miner flow end to end: work
// registration via Seal with local mining disabled, work retrieval via
// eth_getWork, solving the work with nothing but the crypto/randomx package
// (proving an external RandomX miner needs no engine internals), and
// submission via eth_submitWork.
func TestRemoteSealer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping RandomX mining test in short mode")
	}
	engine := newTestEngine(t)
	engine.SetThreads(-1) // disable local mining; remote only
	api := &API{engine}

	// No work registered yet.
	if _, err := api.GetWork(); err != errNoMiningWork {
		t.Fatalf("expected errNoMiningWork, got %v", err)
	}

	genesis := testGenesis()
	chain := newTestChain(genesis)

	// A low-difficulty header so the test can solve it in light mode.
	header := &types.Header{
		ParentHash: genesis.Hash(),
		Number:     big.NewInt(1),
		Time:       13,
		GasLimit:   genesis.GasLimit,
		UncleHash:  types.EmptyUncleHash,
		TxHash:     types.EmptyTxsHash,
		Difficulty: big.NewInt(500),
		MixDigest:  genesis.Hash(), // seed commitment (genesis epoch)
	}
	block := types.NewBlockWithHeader(header)

	results := make(chan *types.Block, 1)
	if err := engine.Seal(chain, block, results, nil); err != nil {
		t.Fatal(err)
	}

	work, err := api.GetWork()
	if err != nil {
		t.Fatal(err)
	}
	sealHash := engine.SealHash(header)
	if work[0] != sealHash.Hex() {
		t.Fatalf("work sealhash: got %s, want %s", work[0], sealHash.Hex())
	}
	if work[1] != header.MixDigest.Hex() {
		t.Fatalf("work seedhash: got %s, want %s", work[1], header.MixDigest.Hex())
	}

	// Solve the work package externally, using only the published fields.
	seed := common.HexToHash(work[1])
	target := new(big.Int).SetBytes(common.HexToHash(work[2]).Bytes())

	flags := randomx.GetFlags() | randomx.FlagV2
	cache, err := randomx.AllocCache(flags)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Release()
	if err := cache.Init(seed.Bytes()); err != nil {
		t.Fatal(err)
	}
	vm, err := randomx.CreateVM(flags, cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vm.Destroy()

	var solution uint64
	input := sealInput(common.HexToHash(work[0]), types.BlockNonce{})
	for nonce := uint64(0); ; nonce++ {
		bin := types.EncodeNonce(nonce)
		copy(input[common.HashLength:], bin[:])
		hash := vm.CalcHash(input)
		if new(big.Int).SetBytes(hash[:]).Cmp(target) <= 0 {
			solution = nonce
			break
		}
	}

	// A wrong solution must be rejected.
	if api.SubmitWork(types.EncodeNonce(solution+1), sealHash, common.Hash{}) {
		t.Fatal("bogus solution accepted")
	}
	// A solution against unknown work must be rejected.
	if api.SubmitWork(types.EncodeNonce(solution), common.HexToHash("0xbad"), common.Hash{}) {
		t.Fatal("solution for unknown work accepted")
	}
	// A wrong seed-hash echo must be rejected.
	if api.SubmitWork(types.EncodeNonce(solution), sealHash, common.HexToHash("0xdeadbeef")) {
		t.Fatal("solution with wrong seed hash accepted")
	}
	// The real solution must be accepted and delivered.
	if !api.SubmitWork(types.EncodeNonce(solution), sealHash, header.MixDigest) {
		t.Fatal("valid solution rejected")
	}
	select {
	case sealed := <-results:
		if sealed.Nonce() != solution {
			t.Fatalf("sealed nonce: got %d, want %d", sealed.Nonce(), solution)
		}
		if err := engine.verifySeal(chain, sealed.Header(), genesis); err != nil {
			t.Fatalf("remote-sealed block fails verification: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("sealed block not delivered")
	}
}

// TestStaleSubmission checks that solutions for sufficiently old work are
// rejected once newer work has replaced it. The engine runs with noverify
// (mirroring ethash's stale-submission test) so no real hashing is needed.
func TestStaleSubmission(t *testing.T) {
	engine := New(&Config{EpochLength: 8, EpochLag: 2}, nil, true)
	engine.SetThreads(-1) // disable local mining; remote only
	t.Cleanup(func() { engine.Close() })
	api := &API{engine}

	genesis := testGenesis()
	chain := newTestChain(genesis)

	makeHeader := func(number uint64) *types.Header {
		return &types.Header{
			ParentHash: genesis.Hash(),
			Number:     new(big.Int).SetUint64(number),
			Time:       number * 13,
			GasLimit:   genesis.GasLimit,
			UncleHash:  types.EmptyUncleHash,
			TxHash:     types.EmptyTxsHash,
			Difficulty: big.NewInt(500),
			MixDigest:  genesis.Hash(),
		}
	}

	results := make(chan *types.Block, staleThreshold*2)
	first := makeHeader(1)
	if err := engine.Seal(chain, types.NewBlockWithHeader(first), results, nil); err != nil {
		t.Fatal(err)
	}
	// Move the current work far past the first one.
	last := makeHeader(1 + staleThreshold)
	if err := engine.Seal(chain, types.NewBlockWithHeader(last), results, nil); err != nil {
		t.Fatal(err)
	}

	if api.SubmitWork(types.EncodeNonce(7), engine.SealHash(first), common.Hash{}) {
		t.Fatal("stale solution accepted")
	}
	if !api.SubmitWork(types.EncodeNonce(7), engine.SealHash(last), common.Hash{}) {
		t.Fatal("current solution rejected")
	}
	select {
	case sealed := <-results:
		if got, want := sealed.NumberU64(), last.Number.Uint64(); got != want {
			t.Fatalf("delivered block number: got %d, want %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("accepted solution not delivered")
	}
}
