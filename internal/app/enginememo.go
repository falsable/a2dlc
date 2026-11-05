package app

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"

	"github.com/falsable/a2dlc/internal/contract"
)

// EngineMemo keeps, for the life of one process, the engines' answers to the differential (operation diff) by what was
// asked: the base commit, the candidate tree, the engine's config and the whole request. a2dlc run calls the oracle more
// than once on the same candidate (each Saboteur round re-checks the tree with the overlay of new tests, which do not
// change what the engines compare), and without it each round pays the engines' whole budget again for the same answer.
//
// It lives in memory on purpose. A cache on disk outside the repo, like the suite cache, is writable by the agent that
// is being judged: a planted PASS would be a bypass of the gate itself, not of one test run. A process the agent did not
// start cannot be planted into, and the receipt says which answers were reused (timings.engine_ops "diff_from_memo").
//
// Only decisive answers are kept: no engine error, every target PASS or FAIL, and no function cut by the budget. An
// INCONCLUSIVE can come from a timeout or a crowded machine and may not repeat, so it is asked again.
type EngineMemo struct {
	mu      sync.Mutex
	answers map[string]contract.Response
}

// NewEngineMemo returns an empty memo. A nil *EngineMemo is valid and remembers nothing.
func NewEngineMemo() *EngineMemo { return &EngineMemo{answers: map[string]contract.Response{}} }

func engineMemoKey(baseSha, tree string, e contract.EngineConfig, req contract.Request) (string, bool) {
	eb, rb, err := diffKey(e, req)
	if err != nil {
		return "", false
	}
	h := sha256.New()
	h.Write([]byte(baseSha))
	h.Write([]byte{0})
	h.Write([]byte(tree))
	h.Write([]byte{0})
	h.Write(eb)
	h.Write([]byte{0})
	h.Write(rb)
	return hex.EncodeToString(h.Sum(nil)), true
}

// decisive says whether resp can be reused: it answered, and every target it judged is PASS or FAIL with nothing cut.
func decisive(resp contract.Response) bool {
	if resp.Error != nil || len(resp.EquivalenceUnsupported) > 0 {
		return false
	}
	if resp.DiffBudget != nil && len(resp.DiffBudget.Cut) > 0 {
		return false
	}
	for _, t := range resp.Targets {
		if t.Verdict != contract.PASS && t.Verdict != contract.FAIL {
			return false
		}
	}
	return true
}

// copyResponse copies the lists Check marks in place (MarkCut, unsupported relaxations, waivers), so neither the stored
// answer nor the caller's copy sees the other's changes.
func copyResponse(r contract.Response) contract.Response {
	r.Targets = append([]contract.TargetResult{}, r.Targets...)
	r.OutOfScope = append([]contract.OutOfScope{}, r.OutOfScope...)
	r.Identical = append([]string(nil), r.Identical...)
	return r
}

func (m *EngineMemo) get(baseSha, tree string, e contract.EngineConfig, req contract.Request) (contract.Response, bool) {
	if m == nil {
		return contract.Response{}, false
	}
	key, ok := engineMemoKey(baseSha, tree, e, req)
	if !ok {
		return contract.Response{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.answers[key]
	if !ok {
		return contract.Response{}, false
	}
	return copyResponse(r), true
}

func (m *EngineMemo) put(baseSha, tree string, e contract.EngineConfig, req contract.Request, resp contract.Response) {
	if m == nil || !decisive(resp) {
		return
	}
	key, ok := engineMemoKey(baseSha, tree, e, req)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answers[key] = copyResponse(resp)
}

// engineJobs is how many engines the check runs at once: every engine by default (a polyglot diff waits for the
// slowest engine, not for their sum), 1 to run them one after another. Each engine's container keeps its own memory and
// CPU caps (config.engines[].memory, cpus), so running them side by side adds those caps up: A2DLC_ENGINE_JOBS bounds it.
func engineJobs(n int, env string) int {
	if n < 1 {
		return 1
	}
	j, err := strconv.Atoi(env)
	if err != nil || j < 1 || j > n {
		return n
	}
	return j
}
