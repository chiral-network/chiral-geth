// Package randomx implements the RandomX v2 proof-of-work consensus engine
// used by the Chiral network.
//
// The PoW hash of a block is
//
//	RandomX_v2(key, SealHash(header) || nonce)
//
// where key is the header hash of the block's "seed block": seed epochs are
// EpochLength blocks long and the key for an epoch is the hash of the last
// block that is at least EpochLag blocks older than the epoch's first block
// (mirroring Monero's key-block schedule). The first EpochLength+EpochLag
// blocks are keyed by the genesis hash. A header additionally commits to its
// seed hash in the MixDigest field, giving verifiers and external miners an
// explicit, cheaply-checkable key.
//
// Verification runs in RandomX light mode (~256 MiB cache per epoch, LRU of
// recent epochs); mining builds the full ~2.1 GiB dataset per epoch.
package randomx

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/randomx"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	// EpochLength is the default seed epoch length: the RandomX key changes
	// every EpochLength blocks.
	EpochLength = 2048
	// EpochLag is the default distance between a seed block and the first
	// block keyed by it, giving miners time to precompute the next dataset.
	EpochLag = 64

	// maxCachedEpochs bounds the verification caches kept in memory
	// (~256 MiB each): the current epoch, its neighbors around a boundary,
	// and one for deep-reorg verification.
	maxCachedEpochs = 3
	// maxDatasets bounds the mining datasets kept in memory (~2.1 GiB
	// each): the current epoch and the next one during a transition.
	maxDatasets = 2
	// vmPoolSize bounds the idle light-mode VMs retained per epoch cache.
	vmPoolSize = 8
)

// Config are the configuration parameters of the RandomX engine.
type Config struct {
	FakeMode  bool          // accept any seal as valid (block import tests)
	FullFake  bool          // additionally skip all header rule checks
	FakeFail  uint64        // block number whose seal verification fails in fake mode
	FakeDelay time.Duration // sleep delay before returning fake verification results

	// EpochLength/EpochLag override the seed epoch schedule; zero values
	// select the Chiral defaults. Tests use short epochs to exercise epoch
	// transitions cheaply.
	EpochLength uint64
	EpochLag    uint64

	// Flags force a specific RandomX flag set (plus FlagV2, always added).
	// Zero means auto-detect: JIT and hardware AES where available, W^X
	// enforced on JIT memory.
	Flags randomx.Flags

	Log  log.Logger
	Rand *rand.Rand
}

// RandomX is a consensus.Engine implementing PoW with the RandomX v2 VM.
type RandomX struct {
	config Config

	log      log.Logger
	lock     sync.Mutex // protects threads and rand
	rand     *rand.Rand
	threads  int
	update   chan struct{}
	hashrate metrics.Meter
	remote   *remoteSealer

	flags randomx.Flags // resolved flag set (always includes FlagV2)

	mu       sync.Mutex // protects caches and datasets maps
	caches   map[common.Hash]*epochCache
	datasets map[common.Hash]*minerDataset

	closeOnce sync.Once
}

// ConfigForChain returns an engine Config with the seed epoch schedule taken
// from the chain configuration; nil chain values select the engine defaults.
func ConfigForChain(conf ctypes.ChainConfigurator) *Config {
	c := &Config{}
	if n := conf.GetRandomXSeedEpochLength(); n != nil {
		c.EpochLength = *n
	}
	if n := conf.GetRandomXSeedEpochLag(); n != nil {
		c.EpochLag = *n
	}
	return c
}

// New creates a RandomX consensus engine.
func New(config *Config, notify []string, noverify bool) *RandomX {
	if config == nil {
		config = &Config{}
	}
	r := &RandomX{
		config:   *config,
		log:      log.Root(),
		update:   make(chan struct{}),
		hashrate: metrics.NewMeter(),
		caches:   make(map[common.Hash]*epochCache),
		datasets: make(map[common.Hash]*minerDataset),
	}
	if config.Log != nil {
		r.log = config.Log
	}
	if config.Rand != nil {
		r.rand = config.Rand
	}
	r.flags = config.Flags
	if r.flags == 0 {
		r.flags = randomx.GetFlags()
		if r.flags&randomx.FlagJIT != 0 {
			r.flags |= randomx.FlagSecure
		}
	}
	r.flags |= randomx.FlagV2
	r.remote = startRemoteSealer(r, notify, noverify)
	return r
}

// NewFaker creates an engine that accepts all seals and applies all other
// consensus rules; used by tests and --dev style setups.
func NewFaker() *RandomX {
	return newFake(Config{FakeMode: true})
}

// NewFakeFailer creates a fake engine that fails seal verification at the
// given block number.
func NewFakeFailer(fail uint64) *RandomX {
	return newFake(Config{FakeMode: true, FakeFail: fail})
}

// NewFakeDelayer creates a fake engine that delays verifications by the given
// duration.
func NewFakeDelayer(delay time.Duration) *RandomX {
	return newFake(Config{FakeMode: true, FakeDelay: delay})
}

// NewFullFaker creates a fake engine that skips all verification rules.
func NewFullFaker() *RandomX {
	return newFake(Config{FakeMode: true, FullFake: true})
}

func newFake(config Config) *RandomX {
	return &RandomX{
		config:   config,
		log:      log.Root(),
		update:   make(chan struct{}),
		hashrate: metrics.NewMeter(),
		caches:   make(map[common.Hash]*epochCache),
		datasets: make(map[common.Hash]*minerDataset),
	}
}

// Close terminates background threads and releases all RandomX memory.
func (r *RandomX) Close() error {
	r.closeOnce.Do(func() {
		if r.remote != nil {
			close(r.remote.requestExit)
			<-r.remote.exitCh
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		for seed, entry := range r.caches {
			entry.retire()
			delete(r.caches, seed)
		}
		for seed, ds := range r.datasets {
			ds.retire()
			delete(r.datasets, seed)
		}
	})
	return nil
}

// APIs implements consensus.Engine, returning the user facing RPC APIs.
func (r *RandomX) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return []rpc.API{
		{
			Namespace: "eth",
			Version:   "1.0",
			Service:   &API{r},
			Public:    true,
		},
	}
}

// Hashrate implements consensus.PoW, returning the measured local and remote
// hash rate.
func (r *RandomX) Hashrate() float64 {
	var remote uint64
	if r.remote != nil {
		res := make(chan uint64, 1)
		select {
		case r.remote.fetchRateCh <- res:
			remote = <-res
		case <-r.remote.exitCh:
		}
	}
	return r.hashrate.Snapshot().Rate1() + float64(remote)
}

// Threads returns the number of mining threads currently enabled.
func (r *RandomX) Threads() int {
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.threads
}

// SetThreads updates the number of mining threads. Zero uses all cores,
// negative counts disable local mining.
func (r *RandomX) SetThreads(threads int) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.threads = threads
	select {
	case r.update <- struct{}{}:
	default:
	}
}

// epochLength returns the configured seed epoch length.
func (r *RandomX) epochLength() uint64 {
	if r.config.EpochLength != 0 {
		return r.config.EpochLength
	}
	return EpochLength
}

// epochLag returns the configured seed lag.
func (r *RandomX) epochLag() uint64 {
	if r.config.EpochLag != 0 {
		return r.config.EpochLag
	}
	return EpochLag
}

// seedBlockNumber returns the number of the block whose header hash keys the
// RandomX cache for block height number.
func (r *RandomX) seedBlockNumber(number uint64) uint64 {
	length, lag := r.epochLength(), r.epochLag()
	if number <= length+lag {
		return 0
	}
	return ((number - lag - 1) / length) * length
}

// seedHash returns the RandomX key for block `number`, following the ancestry
// of `parent` (the header at number-1) so that sidechain headers resolve
// their seed on their own branch. The walk shortcuts to a direct canonical
// lookup as soon as it rejoins the canonical chain, so it is O(fork depth),
// not O(epoch length), in the common case.
func (r *RandomX) seedHash(chain consensus.ChainHeaderReader, number uint64, parent *types.Header) (common.Hash, error) {
	seedNum := r.seedBlockNumber(number)
	if seedNum == 0 {
		genesis := chain.GetHeaderByNumber(0)
		if genesis == nil {
			return common.Hash{}, consensus.ErrUnknownAncestor
		}
		return genesis.Hash(), nil
	}
	header := parent
	for header.Number.Uint64() > seedNum {
		num := header.Number.Uint64()
		if canon := chain.GetHeaderByNumber(num); canon != nil && canon.Hash() == header.Hash() {
			seed := chain.GetHeaderByNumber(seedNum)
			if seed == nil {
				return common.Hash{}, consensus.ErrUnknownAncestor
			}
			return seed.Hash(), nil
		}
		if header = chain.GetHeader(header.ParentHash, num-1); header == nil {
			return common.Hash{}, consensus.ErrUnknownAncestor
		}
	}
	return header.Hash(), nil
}

// epochCache wraps the light-mode cache of one seed epoch together with a
// small pool of VMs bound to it.
type epochCache struct {
	seed  common.Hash
	flags randomx.Flags

	once  sync.Once
	cache *randomx.Cache
	err   error

	mu      sync.Mutex
	vms     []*randomx.VM
	inUse   int
	retired bool
	lastUse time.Time
}

// generate initializes the cache; concurrent callers block until done.
func (e *epochCache) generate(logger log.Logger) {
	e.once.Do(func() {
		start := time.Now()
		cache, err := randomx.AllocCache(e.flags)
		if err != nil {
			e.err = err
			return
		}
		if err := cache.Init(e.seed.Bytes()); err != nil {
			cache.Release()
			e.err = err
			return
		}
		e.cache = cache
		logger.Debug("Generated RandomX verification cache", "seed", e.seed, "elapsed", common.PrettyDuration(time.Since(start)))
	})
}

// hash computes the RandomX hash of input against this epoch's cache using a
// pooled light-mode VM.
func (e *epochCache) hash(input []byte) ([randomx.HashSize]byte, error) {
	e.mu.Lock()
	if e.retired || e.cache == nil {
		e.mu.Unlock()
		return [randomx.HashSize]byte{}, errEpochRetired
	}
	var vm *randomx.VM
	if n := len(e.vms); n > 0 {
		vm, e.vms = e.vms[n-1], e.vms[:n-1]
	}
	e.inUse++
	e.lastUse = time.Now()
	e.mu.Unlock()

	if vm == nil {
		var err error
		vm, err = randomx.CreateVM(e.flags, e.cache, nil)
		if err != nil {
			e.mu.Lock()
			e.inUse--
			e.mu.Unlock()
			return [randomx.HashSize]byte{}, err
		}
	}
	out := vm.CalcHash(input)

	e.mu.Lock()
	e.inUse--
	if e.retired {
		vm.Destroy()
		if e.inUse == 0 {
			e.releaseLocked()
		}
	} else if len(e.vms) < vmPoolSize {
		e.vms = append(e.vms, vm)
	} else {
		vm.Destroy()
	}
	e.mu.Unlock()
	return out, nil
}

// retire marks the epoch for teardown; memory is released once no hash is in
// flight.
func (e *epochCache) retire() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.retired {
		return
	}
	e.retired = true
	for _, vm := range e.vms {
		vm.Destroy()
	}
	e.vms = nil
	if e.inUse == 0 {
		e.releaseLocked()
	}
}

func (e *epochCache) releaseLocked() {
	if e.cache != nil {
		e.cache.Release()
		e.cache = nil
	}
}

var (
	errEpochRetired = errors.New("randomx: epoch cache retired during use")
)

// verifyHash computes the light-mode RandomX hash of input keyed by seed,
// creating (and LRU-evicting) epoch caches as needed.
func (r *RandomX) verifyHash(seed common.Hash, input []byte) ([randomx.HashSize]byte, error) {
	r.mu.Lock()
	entry, ok := r.caches[seed]
	if !ok {
		entry = &epochCache{seed: seed, flags: r.flags, lastUse: time.Now()}
		r.caches[seed] = entry
		// Evict the least recently used epoch beyond the cap.
		for len(r.caches) > maxCachedEpochs {
			var oldest *epochCache
			for _, e := range r.caches {
				if e == entry {
					continue
				}
				if oldest == nil || e.lastUse.Before(oldest.lastUse) {
					oldest = e
				}
			}
			if oldest == nil {
				break
			}
			delete(r.caches, oldest.seed)
			oldest.retire()
		}
	}
	r.mu.Unlock()

	entry.generate(r.log)
	if entry.err != nil {
		return [randomx.HashSize]byte{}, entry.err
	}
	return entry.hash(input)
}

// minerDataset wraps the fast-mode dataset of one seed epoch.
type minerDataset struct {
	seed  common.Hash
	flags randomx.Flags

	once    sync.Once
	dataset *randomx.Dataset
	err     error

	mu      sync.Mutex
	inUse   int
	retired bool
	lastUse time.Time
}

func (d *minerDataset) generate(logger log.Logger) {
	d.once.Do(func() {
		start := time.Now()
		logger.Info("Generating RandomX mining dataset", "seed", d.seed)
		cache, err := randomx.AllocCache(d.flags)
		if err != nil {
			d.err = err
			return
		}
		defer cache.Release()
		if err := cache.Init(d.seed.Bytes()); err != nil {
			d.err = err
			return
		}
		dataset, err := randomx.AllocDataset(d.flags)
		if err != nil {
			d.err = err
			return
		}
		if err := dataset.InitFull(cache, 0); err != nil {
			dataset.Release()
			d.err = err
			return
		}
		d.dataset = dataset
		logger.Info("Generated RandomX mining dataset", "seed", d.seed, "elapsed", common.PrettyDuration(time.Since(start)))
	})
}

// acquire pins the dataset against release; pair with release.
func (d *minerDataset) acquire() {
	d.mu.Lock()
	d.inUse++
	d.lastUse = time.Now()
	d.mu.Unlock()
}

func (d *minerDataset) release() {
	d.mu.Lock()
	d.inUse--
	if d.retired && d.inUse == 0 && d.dataset != nil {
		d.dataset.Release()
		d.dataset = nil
	}
	d.mu.Unlock()
}

func (d *minerDataset) retire() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.retired {
		return
	}
	d.retired = true
	if d.inUse == 0 && d.dataset != nil {
		d.dataset.Release()
		d.dataset = nil
	}
}

// minerDatasetFor returns the (generated) mining dataset for the given seed,
// building it on first use and evicting stale epochs beyond the cap. The
// returned dataset is acquired; the caller must release it.
func (r *RandomX) minerDatasetFor(seed common.Hash) (*minerDataset, error) {
	r.mu.Lock()
	ds, ok := r.datasets[seed]
	if !ok {
		ds = &minerDataset{seed: seed, flags: r.flags, lastUse: time.Now()}
		r.datasets[seed] = ds
		for len(r.datasets) > maxDatasets {
			var oldest *minerDataset
			for _, d := range r.datasets {
				if d == ds {
					continue
				}
				if oldest == nil || d.lastUse.Before(oldest.lastUse) {
					oldest = d
				}
			}
			if oldest == nil {
				break
			}
			delete(r.datasets, oldest.seed)
			oldest.retire()
		}
	}
	r.mu.Unlock()

	ds.generate(r.log)
	if ds.err != nil {
		return nil, ds.err
	}
	ds.acquire()
	return ds, nil
}
