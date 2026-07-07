package randomx

import (
	"errors"
	"fmt"
	"math/big"
	"runtime"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"golang.org/x/crypto/sha3"
)

const (
	maxUncles              = 2                // Maximum number of uncles allowed in a single block
	allowedFutureBlockTime = 15 * time.Second // Max time from current time allowed for blocks, before they're considered future blocks
)

var (
	two256 = new(big.Int).Exp(big.NewInt(2), big.NewInt(256), big.NewInt(0))
	big32  = big.NewInt(32)
)

// Various error messages to mark blocks invalid. These should be private to
// prevent engine specific errors from being referenced in the remainder of the
// codebase, inherently breaking if the engine is swapped out. Please put common
// error types into the consensus package.
var (
	errOlderBlockTime    = errors.New("timestamp older than parent")
	errTooManyUncles     = errors.New("too many uncles")
	errDuplicateUncle    = errors.New("duplicate uncle")
	errUncleIsAncestor   = errors.New("uncle is ancestor")
	errDanglingUncle     = errors.New("uncle's parent is not ancestor")
	errInvalidDifficulty = errors.New("non-positive difficulty")
	errInvalidMixDigest  = errors.New("invalid mix digest (seed hash commitment)")
	errInvalidPoW        = errors.New("invalid proof-of-work")
)

// Author implements consensus.Engine, returning the header's coinbase as the
// proof-of-work verified author of the block.
func (r *RandomX) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader checks whether a header conforms to the consensus rules of the
// RandomX engine.
func (r *RandomX) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header, seal bool) error {
	// If we're running a full engine faking, accept any input as valid
	if r.config.FullFake {
		return nil
	}
	// Short circuit if the header is known, or its parent not
	number := header.Number.Uint64()
	if chain.GetHeader(header.Hash(), number) != nil {
		return nil
	}
	parent := chain.GetHeader(header.ParentHash, number-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	// Sanity checks passed, do a proper verification
	return r.verifyHeader(chain, header, parent, false, seal, time.Now().Unix())
}

// VerifyHeaders is similar to VerifyHeader, but verifies a batch of headers
// concurrently. The method returns a quit channel to abort the operations and
// a results channel to retrieve the async verifications.
func (r *RandomX) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error) {
	// If we're running a full engine faking, accept any input as valid
	if r.config.FullFake || len(headers) == 0 {
		abort, results := make(chan struct{}), make(chan error, len(headers))
		for i := 0; i < len(headers); i++ {
			results <- nil
		}
		return abort, results
	}
	// Batches routinely span more blocks than the seed lag, so a header's
	// seed block may itself still be in the batch rather than in the
	// database. Overlay the batch on the chain reader so seed resolution
	// (and parent lookups) can see it.
	reader := newBatchReader(chain, headers)

	// Spawn as many workers as allowed threads
	workers := runtime.GOMAXPROCS(0)
	if len(headers) < workers {
		workers = len(headers)
	}

	// Create a task channel and spawn the verifiers
	var (
		inputs  = make(chan int)
		done    = make(chan int, workers)
		errs    = make([]error, len(headers))
		abort   = make(chan struct{})
		unixNow = time.Now().Unix()
	)
	for i := 0; i < workers; i++ {
		go func() {
			for index := range inputs {
				errs[index] = r.verifyHeaderWorker(reader, headers, seals, index, unixNow)
				done <- index
			}
		}()
	}

	errorsOut := make(chan error, len(headers))
	go func() {
		defer close(inputs)
		var (
			in, out = 0, 0
			checked = make([]bool, len(headers))
			inputs  = inputs
		)
		for {
			select {
			case inputs <- in:
				if in++; in == len(headers) {
					// Reached end of headers. Stop sending to workers.
					inputs = nil
				}
			case index := <-done:
				for checked[index] = true; checked[out]; out++ {
					errorsOut <- errs[out]
					if out == len(headers)-1 {
						return
					}
				}
			case <-abort:
				return
			}
		}
	}()
	return abort, errorsOut
}

func (r *RandomX) verifyHeaderWorker(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool, index int, unixNow int64) error {
	var parent *types.Header
	if index == 0 {
		parent = chain.GetHeader(headers[0].ParentHash, headers[0].Number.Uint64()-1)
	} else if headers[index-1].Hash() == headers[index].ParentHash {
		parent = headers[index-1]
	}
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	return r.verifyHeader(chain, headers[index], parent, false, seals[index], unixNow)
}

// batchReader overlays a contiguous, parent-linked batch of headers being
// verified on top of the canonical chain, so ancestor lookups can resolve
// headers that have not been written to the database yet.
type batchReader struct {
	consensus.ChainHeaderReader
	base   uint64
	byHash map[common.Hash]int
	batch  []*types.Header
}

func newBatchReader(chain consensus.ChainHeaderReader, headers []*types.Header) consensus.ChainHeaderReader {
	byHash := make(map[common.Hash]int, len(headers))
	for i, h := range headers {
		byHash[h.Hash()] = i
	}
	return &batchReader{
		ChainHeaderReader: chain,
		base:              headers[0].Number.Uint64(),
		byHash:            byHash,
		batch:             headers,
	}
}

func (br *batchReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	if i, ok := br.byHash[hash]; ok && br.batch[i].Number.Uint64() == number {
		return br.batch[i]
	}
	return br.ChainHeaderReader.GetHeader(hash, number)
}

func (br *batchReader) GetHeaderByHash(hash common.Hash) *types.Header {
	if i, ok := br.byHash[hash]; ok {
		return br.batch[i]
	}
	return br.ChainHeaderReader.GetHeaderByHash(hash)
}

// GetHeaderByNumber treats the batch as the pending canonical extension: the
// batch is parent-linked, so if its tail connects to the canonical chain, its
// members are the canonical headers at their numbers once imported.
func (br *batchReader) GetHeaderByNumber(number uint64) *types.Header {
	if number >= br.base && number < br.base+uint64(len(br.batch)) {
		return br.batch[number-br.base]
	}
	return br.ChainHeaderReader.GetHeaderByNumber(number)
}

// VerifyUncles verifies that the given block's uncles conform to the
// consensus rules of the RandomX engine.
func (r *RandomX) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	// If we're running a full engine faking, accept any input as valid
	if r.config.FullFake {
		return nil
	}
	// Verify that there are at most maxUncles uncles included in this block
	if len(block.Uncles()) > maxUncles {
		return errTooManyUncles
	}
	if len(block.Uncles()) == 0 {
		return nil
	}
	// Gather the set of past uncles and ancestors
	uncles, ancestors := mapset.NewSet[common.Hash](), make(map[common.Hash]*types.Header)

	number, parent := block.NumberU64()-1, block.ParentHash()
	for i := 0; i < 7; i++ {
		ancestorHeader := chain.GetHeader(parent, number)
		if ancestorHeader == nil {
			break
		}
		ancestors[ancestorHeader.Hash()] = ancestorHeader
		// If the ancestor doesn't have any uncles, we don't have to iterate them
		if ancestorHeader.UncleHash != types.EmptyUncleHash {
			// Need to add those uncles to the banned list too
			ancestor := chain.GetBlock(parent, number)
			if ancestor == nil {
				break
			}
			for _, uncle := range ancestor.Uncles() {
				uncles.Add(uncle.Hash())
			}
		}
		parent, number = ancestorHeader.ParentHash, number-1
	}
	ancestors[block.Hash()] = block.Header()
	uncles.Add(block.Hash())

	// Verify each of the uncles that it's recent, but not an ancestor
	for _, uncle := range block.Uncles() {
		// Make sure every uncle is rewarded only once
		hash := uncle.Hash()
		if uncles.Contains(hash) {
			return errDuplicateUncle
		}
		uncles.Add(hash)

		// Make sure the uncle has a valid ancestry
		if ancestors[hash] != nil {
			return errUncleIsAncestor
		}
		if ancestors[uncle.ParentHash] == nil || uncle.ParentHash == block.ParentHash() {
			return errDanglingUncle
		}
		if err := r.verifyHeader(chain, uncle, ancestors[uncle.ParentHash], true, true, time.Now().Unix()); err != nil {
			return err
		}
	}
	return nil
}

// verifyHeader checks whether a header conforms to the consensus rules of the
// RandomX engine. See YP section 4.3.4. "Block Header Validity".
func (r *RandomX) verifyHeader(chain consensus.ChainHeaderReader, header, parent *types.Header, uncle bool, seal bool, unixNow int64) error {
	// Ensure that the header's extra-data section is of a reasonable size
	if uint64(len(header.Extra)) > vars.MaximumExtraDataSize {
		return fmt.Errorf("extra-data too long: %d > %d", len(header.Extra), vars.MaximumExtraDataSize)
	}
	// Verify the header's timestamp
	if !uncle {
		if header.Time > uint64(unixNow+int64(allowedFutureBlockTime.Seconds())) {
			return consensus.ErrFutureBlock
		}
	}
	if header.Time <= parent.Time {
		return errOlderBlockTime
	}
	// Verify the block's difficulty based on its timestamp and parent's difficulty
	expected := r.CalcDifficulty(chain, header.Time, parent)
	if expected.Cmp(header.Difficulty) != 0 {
		return fmt.Errorf("invalid difficulty: have %v, want %v", header.Difficulty, expected)
	}
	// Verify that the gas limit is <= 2^63-1
	if header.GasLimit > vars.MaxGasLimit {
		return fmt.Errorf("invalid gasLimit: have %v, max %v", header.GasLimit, vars.MaxGasLimit)
	}
	// Verify that the gasUsed is <= gasLimit
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}
	// Verify that the block number is parent's +1
	if diff := new(big.Int).Sub(header.Number, parent.Number); diff.Cmp(big.NewInt(1)) != 0 {
		return consensus.ErrInvalidNumber
	}
	// Verify the block's gas usage and (if applicable) verify the base fee.
	if !chain.Config().IsEnabled(chain.Config().GetEIP1559Transition, header.Number) {
		// Verify BaseFee not present before EIP-1559 fork.
		if header.BaseFee != nil {
			return fmt.Errorf("invalid baseFee before fork: have %d, expected 'nil'", header.BaseFee)
		}
		if err := misc.VerifyGaslimit(parent.GasLimit, header.GasLimit); err != nil {
			return err
		}
	} else if err := eip1559.VerifyEIP1559Header(chain.Config(), parent, header); err != nil {
		// Verify the header's EIP-1559 attributes.
		return err
	}
	// Chiral is a pure PoW chain: beacon-chain derived header fields must
	// never be set, regardless of fork configuration.
	if header.WithdrawalsHash != nil {
		return fmt.Errorf("invalid withdrawalsHash: have %x, expected nil", header.WithdrawalsHash)
	}
	if header.BlobGasUsed != nil {
		return fmt.Errorf("invalid blobGasUsed: have %v, expected nil", header.BlobGasUsed)
	}
	if header.ExcessBlobGas != nil {
		return fmt.Errorf("invalid excessBlobGas: have %v, expected nil", header.ExcessBlobGas)
	}
	if header.ParentBeaconRoot != nil {
		return fmt.Errorf("invalid parentBeaconRoot: have %x, expected nil", header.ParentBeaconRoot)
	}
	// Verify the engine specific seal securing the block
	if seal {
		if err := r.verifySeal(chain, header, parent); err != nil {
			return err
		}
	}
	// If all checks passed, validate any special fields for hard forks
	if err := misc.VerifyForkHashes(chain.Config(), header, uncle); err != nil {
		return err
	}
	return nil
}

// verifySeal checks whether a block satisfies the PoW difficulty requirements
// and commits to the correct seed hash.
//
// When chain is nil (remote miner submissions against a locally prepared
// header template), the header's MixDigest is trusted as the seed hash; it
// was set by Prepare from chain state.
func (r *RandomX) verifySeal(chain consensus.ChainHeaderReader, header *types.Header, parent *types.Header) error {
	// If we're running a fake PoW, accept any seal as valid
	if r.config.FakeMode {
		time.Sleep(r.config.FakeDelay)
		if r.config.FakeFail == header.Number.Uint64() {
			return errInvalidPoW
		}
		return nil
	}
	// Ensure that we have a valid difficulty for the block
	if header.Difficulty.Sign() <= 0 {
		return errInvalidDifficulty
	}
	seed := header.MixDigest
	if chain != nil {
		expected, err := r.seedHash(chain, header.Number.Uint64(), parent)
		if err != nil {
			return err
		}
		if header.MixDigest != expected {
			return errInvalidMixDigest
		}
		seed = expected
	}
	result, err := r.verifyHash(seed, sealInput(r.SealHash(header), header.Nonce))
	if err != nil {
		return err
	}
	target := new(big.Int).Div(two256, header.Difficulty)
	if new(big.Int).SetBytes(result[:]).Cmp(target) > 0 {
		return errInvalidPoW
	}
	return nil
}

// sealInput assembles the RandomX input for a seal attempt: the 32-byte seal
// hash followed by the 8-byte nonce.
func sealInput(sealHash common.Hash, nonce types.BlockNonce) []byte {
	input := make([]byte, common.HashLength+8)
	copy(input, sealHash.Bytes())
	copy(input[common.HashLength:], nonce[:])
	return input
}

// Prepare implements consensus.Engine, initializing the difficulty and the
// seed-hash commitment (MixDigest) of the header.
func (r *RandomX) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	header.Difficulty = r.CalcDifficulty(chain, header.Time, parent)
	if r.config.FakeMode {
		header.MixDigest = common.Hash{}
		return nil
	}
	seed, err := r.seedHash(chain, header.Number.Uint64(), parent)
	if err != nil {
		return err
	}
	header.MixDigest = seed
	return nil
}

// Finalize implements consensus.Engine, accumulating the block and uncle
// rewards and setting the final state root on the header.
func (r *RandomX) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, withdrawals []*types.Withdrawal) {
	accumulateRewards(chain.Config(), state, header, uncles)
	header.Root = state.IntermediateRoot(chain.Config().IsEnabled(chain.Config().GetEIP161dTransition, header.Number))
}

// FinalizeAndAssemble implements consensus.Engine, accumulating the block and
// uncle rewards, setting the final state and assembling the block.
func (r *RandomX) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction, uncles []*types.Header, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {
	if len(withdrawals) > 0 {
		return nil, errors.New("randomx does not support withdrawals")
	}
	// Finalize block
	r.Finalize(chain, header, state, txs, uncles, nil)

	// Header seems complete, assemble into a block and return
	return types.NewBlock(header, txs, uncles, receipts, trie.NewStackTrie(nil)), nil
}

// SealHash returns the hash of a block prior to it being sealed. The seal
// fields (MixDigest, Nonce) are excluded; everything else, including the
// EIP-1559 base fee, is covered.
func (r *RandomX) SealHash(header *types.Header) (hash common.Hash) {
	hasher := sha3.NewLegacyKeccak256()

	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}
	rlp.Encode(hasher, enc)
	hasher.Sum(hash[:0])
	return hash
}

// Some weird constants to avoid constant memory allocs for them.
var (
	big1       = big.NewInt(1)
	big2       = big.NewInt(2)
	big9       = big.NewInt(9)
	bigMinus20 = big.NewInt(-20)

	// DifficultyBoundDivisor is the bound divisor of the difficulty, used in
	// the update calculations.
	DifficultyBoundDivisor = big.NewInt(200)
	// MinimumDifficulty is the minimum that the difficulty may ever be,
	// scaled for CPU (RandomX) hash rates.
	MinimumDifficulty = big.NewInt(10000)
)

// CalcDifficulty is the difficulty adjustment algorithm. It returns the
// difficulty that a new block should have when created at time given the
// parent block's time and difficulty.
func (r *RandomX) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	return CalcDifficulty(chain.Config(), time, parent)
}

// CalcDifficulty is the EIP-100 family difficulty adjustment algorithm with
// Chiral's constants (no difficulty bomb):
//
//	diff = parent_diff
//	     + (parent_diff / 200) * max((2 if uncles else 1) - (timestamp - parent.timestamp) // 9, -20)
func CalcDifficulty(config ctypes.ChainConfigurator, time uint64, parent *types.Header) *big.Int {
	bigTime := new(big.Int).SetUint64(time)
	bigParentTime := new(big.Int).SetUint64(parent.Time)

	// holds intermediate values to make the algo easier to read & audit
	x := new(big.Int)
	y := new(big.Int)

	// (2 if len(parent_uncles) else 1) - (block_timestamp - parent_timestamp) // 9
	x.Sub(bigTime, bigParentTime)
	x.Div(x, big9)
	if parent.UncleHash == types.EmptyUncleHash {
		x.Sub(big1, x)
	} else {
		x.Sub(big2, x)
	}
	// max(..., -20)
	if x.Cmp(bigMinus20) < 0 {
		x.Set(bigMinus20)
	}
	// parent_diff + (parent_diff / 200) * max(...)
	y.Div(parent.Difficulty, DifficultyBoundDivisor)
	x.Mul(y, x)
	x.Add(parent.Difficulty, x)

	// minimum difficulty can ever be (before exponential factor)
	if x.Cmp(MinimumDifficulty) < 0 {
		x.Set(MinimumDifficulty)
	}
	return x
}
