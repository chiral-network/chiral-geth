package randomx

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/randomx"
)

const (
	// staleThreshold is the maximum depth of the acceptable stale but valid
	// RandomX solution.
	staleThreshold = 7
)

var (
	errNoMiningWork      = errors.New("no mining work available yet")
	errInvalidSealResult = errors.New("invalid or stale proof-of-work solution")
)

// Seal implements consensus.Engine, attempting to find a nonce that satisfies
// the block's difficulty requirements.
func (r *RandomX) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	// If we're running a fake PoW, simply return a 0 nonce immediately
	if r.config.FakeMode {
		header := block.Header()
		header.Nonce = types.BlockNonce{}
		select {
		case results <- block.WithSeal(header):
		default:
			r.log.Warn("Sealing result is not read by miner", "mode", "fake", "sealhash", r.SealHash(block.Header()))
		}
		return nil
	}
	// Create a runner and the multiple search threads it directs
	abort := make(chan struct{})

	r.lock.Lock()
	threads := r.threads
	if r.rand == nil {
		seed, err := crand.Int(crand.Reader, big.NewInt(math.MaxInt64))
		if err != nil {
			r.lock.Unlock()
			return err
		}
		r.rand = rand.New(rand.NewSource(seed.Int64()))
	}
	r.lock.Unlock()
	if threads == 0 {
		threads = runtime.NumCPU()
	}
	if threads < 0 {
		threads = 0 // Allows disabling local mining without extra logic around local/remote
	}
	// Push new work to remote sealer
	if r.remote != nil {
		r.remote.workCh <- &sealTask{block: block, results: results}
	}
	var (
		pend   sync.WaitGroup
		locals = make(chan *types.Block)
	)
	for i := 0; i < threads; i++ {
		pend.Add(1)
		go func(id int, nonce uint64) {
			defer pend.Done()
			r.mine(block, id, nonce, abort, locals)
		}(i, uint64(r.rand.Int63()))
	}
	// Wait until sealing is terminated or a nonce is found
	go func() {
		var result *types.Block
		select {
		case <-stop:
			// Outside abort, stop all miner threads
			close(abort)
		case result = <-locals:
			// One of the threads found a block, abort all others
			select {
			case results <- result:
			default:
				r.log.Warn("Sealing result is not read by miner", "mode", "local", "sealhash", r.SealHash(block.Header()))
			}
			close(abort)
		case <-r.update:
			// Thread count was changed on user request, restart
			close(abort)
			if err := r.Seal(chain, block, results, stop); err != nil {
				r.log.Error("Failed to restart sealing after update", "err", err)
			}
		}
		// Wait for all miners to terminate and return the block
		pend.Wait()
	}()
	return nil
}

// mine is the actual proof-of-work miner that searches for a nonce starting
// from seed that results in a hash below the block's difficulty target. It
// hashes in fast mode against the full dataset of the block's seed epoch.
func (r *RandomX) mine(block *types.Block, id int, seed uint64, abort chan struct{}, found chan *types.Block) {
	var (
		header   = block.Header()
		target   = new(big.Int).Div(two256, header.Difficulty)
		sealHash = r.SealHash(header)
		input    = sealInput(sealHash, types.BlockNonce{})
		result   = new(big.Int)
	)
	logger := r.log.New("miner", id)

	// The dataset build blocks all threads on first use of an epoch; the
	// epoch's seed hash was committed to the header in Prepare.
	dataset, err := r.minerDatasetFor(header.MixDigest)
	if err != nil {
		logger.Error("Failed to build RandomX mining dataset", "seed", header.MixDigest, "err", err)
		return
	}
	defer dataset.release()

	vm, err := randomx.CreateVM(r.flags|randomx.FlagFullMem, nil, dataset.dataset)
	if err != nil {
		logger.Error("Failed to create RandomX mining VM", "err", err)
		return
	}
	defer vm.Destroy()

	// Start generating random nonces until we abort or find a good one. The
	// batched hash API pipelines scratchpad initialization of the next nonce
	// with program execution of the previous one: HashNext(n+1) returns the
	// hash of nonce n.
	var (
		attempts = int64(0)
		nonce    = seed
		prev     = seed
	)
	logger.Trace("Started RandomX nonce search", "seed", seed)

	binary := types.EncodeNonce(nonce)
	copy(input[common.HashLength:], binary[:])
	vm.HashFirst(input)
	nonce++
search:
	for {
		select {
		case <-abort:
			// Mining terminated, update stats and abort
			logger.Trace("RandomX nonce search aborted", "attempts", nonce-seed)
			r.hashrate.Mark(attempts)
			break search

		default:
			// We don't have to update hash rate on every nonce, so update after 2^X nonces
			attempts++
			if (attempts % (1 << 4)) == 0 {
				r.hashrate.Mark(attempts)
				attempts = 0
			}
			// Queue the next nonce, collecting the PoW value of the previous one
			binary := types.EncodeNonce(nonce)
			copy(input[common.HashLength:], binary[:])
			hash := vm.HashNext(input)
			if result.SetBytes(hash[:]).Cmp(target) <= 0 {
				// Correct nonce found, create a new header with it
				header = types.CopyHeader(header)
				header.Nonce = types.EncodeNonce(prev)

				// Seal and return a block (if still needed)
				select {
				case found <- block.WithSeal(header):
					logger.Trace("RandomX nonce found and reported", "attempts", nonce-seed, "nonce", prev)
				case <-abort:
					logger.Trace("RandomX nonce found but discarded", "attempts", nonce-seed, "nonce", prev)
				}
				break search
			}
			prev = nonce
			nonce++
		}
	}
}

// This is the timeout for HTTP requests to notify external miners.
const remoteSealerTimeout = 1 * time.Second

type remoteSealer struct {
	works        map[common.Hash]*types.Block
	rates        map[common.Hash]hashrate
	currentBlock *types.Block
	currentWork  [4]string
	notifyCtx    context.Context
	cancelNotify context.CancelFunc // cancels all notification requests
	reqWG        sync.WaitGroup     // tracks notification request goroutines

	randx        *RandomX
	noverify     bool
	notifyURLs   []string
	results      chan<- *types.Block
	workCh       chan *sealTask   // Notification channel to push new work and relative result channel to remote sealer
	fetchWorkCh  chan *sealWork   // Channel used for remote sealer to fetch mining work
	submitWorkCh chan *mineResult // Channel used for remote sealer to submit their mining result
	fetchRateCh  chan chan uint64 // Channel used to gather submitted hash rate for local or remote sealer.
	submitRateCh chan *hashrate   // Channel used for remote sealer to submit their mining hashrate
	requestExit  chan struct{}
	exitCh       chan struct{}
}

// sealTask wraps a seal block with relative result channel for remote sealer thread.
type sealTask struct {
	block   *types.Block
	results chan<- *types.Block
}

// mineResult wraps the pow solution parameters for the specified block.
type mineResult struct {
	nonce     types.BlockNonce
	mixDigest common.Hash
	hash      common.Hash

	errc chan error
}

// hashrate wraps the hash rate submitted by the remote sealer.
type hashrate struct {
	id   common.Hash
	ping time.Time
	rate uint64

	done chan struct{}
}

// sealWork wraps a seal work package for remote sealer.
type sealWork struct {
	errc chan error
	res  chan [4]string
}

func startRemoteSealer(randx *RandomX, urls []string, noverify bool) *remoteSealer {
	ctx, cancel := context.WithCancel(context.Background())
	s := &remoteSealer{
		randx:        randx,
		noverify:     noverify,
		notifyURLs:   urls,
		notifyCtx:    ctx,
		cancelNotify: cancel,
		works:        make(map[common.Hash]*types.Block),
		rates:        make(map[common.Hash]hashrate),
		workCh:       make(chan *sealTask),
		fetchWorkCh:  make(chan *sealWork),
		submitWorkCh: make(chan *mineResult),
		fetchRateCh:  make(chan chan uint64),
		submitRateCh: make(chan *hashrate),
		requestExit:  make(chan struct{}),
		exitCh:       make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *remoteSealer) loop() {
	defer func() {
		s.randx.log.Trace("RandomX remote sealer is exiting")
		s.cancelNotify()
		s.reqWG.Wait()
		close(s.exitCh)
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case work := <-s.workCh:
			// Update current work with new received block.
			// Note same work can be past twice, happens when changing CPU threads.
			s.results = work.results
			s.makeWork(work.block)
			s.notifyWork()

		case work := <-s.fetchWorkCh:
			// Return current mining work to remote miner.
			if s.currentBlock == nil {
				work.errc <- errNoMiningWork
			} else {
				work.res <- s.currentWork
			}

		case result := <-s.submitWorkCh:
			// Verify submitted PoW solution based on maintained mining blocks.
			if s.submitWork(result.nonce, result.mixDigest, result.hash) {
				result.errc <- nil
			} else {
				result.errc <- errInvalidSealResult
			}

		case result := <-s.submitRateCh:
			// Trace remote sealer's hash rate by submitted value.
			s.rates[result.id] = hashrate{rate: result.rate, ping: time.Now()}
			close(result.done)

		case req := <-s.fetchRateCh:
			// Gather all hash rate submitted by remote sealer.
			var total uint64
			for _, rate := range s.rates {
				// this could overflow
				total += rate.rate
			}
			req <- total

		case <-ticker.C:
			// Clear stale submitted hash rate.
			for id, rate := range s.rates {
				if time.Since(rate.ping) > 10*time.Second {
					delete(s.rates, id)
				}
			}
			// Clear stale pending blocks
			if s.currentBlock != nil {
				for hash, block := range s.works {
					if block.NumberU64()+staleThreshold <= s.currentBlock.NumberU64() {
						delete(s.works, hash)
					}
				}
			}

		case <-s.requestExit:
			return
		}
	}
}

// makeWork creates a work package for external miner.
//
// The work package consists of 4 strings:
//
//	result[0], 32 bytes hex encoded current block header pow-hash (seal hash)
//	result[1], 32 bytes hex encoded seed hash: the RandomX key of the block's epoch
//	result[2], 32 bytes hex encoded boundary condition ("target"), 2^256/difficulty
//	result[3], hex encoded block number
//
// The miner searches for an 8-byte nonce such that
// RandomX_v2(key, sealhash || nonce) <= target.
func (s *remoteSealer) makeWork(block *types.Block) {
	hash := s.randx.SealHash(block.Header())
	s.currentWork[0] = hash.Hex()
	s.currentWork[1] = block.MixDigest().Hex()
	s.currentWork[2] = common.BytesToHash(new(big.Int).Div(two256, block.Difficulty()).Bytes()).Hex()
	s.currentWork[3] = hexutil.EncodeBig(block.Number())

	// Trace the seal work fetched by remote sealer.
	s.currentBlock = block
	s.works[hash] = block
}

// notifyWork notifies all the specified mining endpoints of the availability of
// new work to be processed.
func (s *remoteSealer) notifyWork() {
	work := s.currentWork
	blob, _ := json.Marshal(work)
	s.reqWG.Add(len(s.notifyURLs))
	for _, url := range s.notifyURLs {
		go s.sendNotification(s.notifyCtx, url, blob, work)
	}
}

func (s *remoteSealer) sendNotification(ctx context.Context, url string, json []byte, work [4]string) {
	defer s.reqWG.Done()

	req, err := http.NewRequest("POST", url, bytes.NewReader(json))
	if err != nil {
		s.randx.log.Warn("Can't create remote miner notification", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remoteSealerTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.randx.log.Warn("Failed to notify remote miner", "err", err)
	} else {
		s.randx.log.Trace("Notified remote miner", "miner", url, "hash", work[0], "target", work[2])
		resp.Body.Close()
	}
}

// submitWork verifies the submitted pow solution, returning whether the
// solution was accepted or not (not can be both a bad pow as well as any
// other error, like no pending work or stale mining result).
//
// The mix parameter of eth_submitWork is redundant for RandomX (the header
// template already commits to the seed hash); a non-zero value must match
// the template's commitment.
func (s *remoteSealer) submitWork(nonce types.BlockNonce, mixDigest common.Hash, sealhash common.Hash) bool {
	if s.currentBlock == nil {
		s.randx.log.Error("Pending work without block", "sealhash", sealhash)
		return false
	}
	// Make sure the work submitted is present
	block := s.works[sealhash]
	if block == nil {
		s.randx.log.Warn("Work submitted but none pending", "sealhash", sealhash, "curnumber", s.currentBlock.NumberU64())
		return false
	}
	header := block.Header()
	if (mixDigest != common.Hash{}) && mixDigest != header.MixDigest {
		s.randx.log.Warn("Work submitted with wrong seed hash", "sealhash", sealhash, "have", mixDigest, "want", header.MixDigest)
		return false
	}
	// Verify the correctness of submitted result. The header's MixDigest
	// commitment was set by Prepare from chain state, so seal verification
	// can trust it without chain access.
	header.Nonce = nonce

	start := time.Now()
	if !s.noverify {
		if err := s.randx.verifySeal(nil, header, nil); err != nil {
			s.randx.log.Warn("Invalid proof-of-work submitted", "sealhash", sealhash, "elapsed", common.PrettyDuration(time.Since(start)), "err", err)
			return false
		}
	}
	// Make sure the result channel is assigned.
	if s.results == nil {
		s.randx.log.Warn("RandomX result channel is empty, submitted mining result is rejected")
		return false
	}
	s.randx.log.Trace("Verified correct proof-of-work", "sealhash", sealhash, "elapsed", common.PrettyDuration(time.Since(start)))

	// Solutions seems to be valid, return to the miner and notify acceptance.
	solution := block.WithSeal(header)

	// The submitted solution is within the scope of acceptance.
	if solution.NumberU64()+staleThreshold > s.currentBlock.NumberU64() {
		select {
		case s.results <- solution:
			s.randx.log.Debug("Work submitted is acceptable", "number", solution.NumberU64(), "sealhash", sealhash, "hash", solution.Hash())
			return true
		default:
			s.randx.log.Warn("Sealing result is not read by miner", "mode", "remote", "sealhash", sealhash)
			return false
		}
	}
	// The submitted block is too old to accept, drop it.
	s.randx.log.Warn("Work submitted is too old", "number", solution.NumberU64(), "sealhash", sealhash, "hash", solution.Hash())
	return false
}
