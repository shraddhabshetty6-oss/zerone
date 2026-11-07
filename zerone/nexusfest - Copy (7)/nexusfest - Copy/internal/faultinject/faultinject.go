//go:build faultinject

package faultinject

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type armedPoint struct {
	spec     FailpointSpec
	hits     int
	fired    uint64
	rng      *rand.Rand
	action   string
	partSize int
	delayMs  int
	once     bool
}

var (
	mu       sync.Mutex
	points   = make(map[string]*armedPoint)
	counters = make(map[string]uint64)
	dataDir  string
)

func init() {
	if env := os.Getenv("STUDENTDB_FAILPOINTS"); env != "" {
		_ = Arm([]byte(env))
	}
}

// SetDataDir sets the database directory for writing durability.ledger.
func SetDataDir(dir string) {
	mu.Lock()
	defer mu.Unlock()
	dataDir = dir
}

// Arm parses JSON specifications and arms failpoints.
func Arm(specData json.RawMessage) error {
	mu.Lock()
	defer mu.Unlock()

	var specs map[string]FailpointSpec
	if err := json.Unmarshal(specData, &specs); err != nil {
		return fmt.Errorf("invalid failpoint spec JSON: %w", err)
	}

	for name, spec := range specs {
		if _, ok := Catalog[name]; !ok {
			return fmt.Errorf("unknown failpoint name: %q", name)
		}

		ap := &armedPoint{
			spec: spec,
			rng:  rand.New(rand.NewSource(spec.Seed)),
			once: true,
		}
		if spec.Once != nil {
			ap.once = *spec.Once
		}

		// Parse action
		act := strings.TrimSpace(spec.Action)
		if strings.HasPrefix(act, "partial:") {
			nStr := strings.TrimPrefix(act, "partial:")
			n, err := strconv.Atoi(nStr)
			if err != nil || n < 0 {
				return fmt.Errorf("invalid partial write byte count: %q", nStr)
			}
			ap.action = "partial"
			ap.partSize = n
		} else if strings.HasPrefix(act, "delay:") {
			dStr := strings.TrimPrefix(act, "delay:")
			d, err := strconv.Atoi(dStr)
			if err != nil || d < 0 {
				return fmt.Errorf("invalid delay ms: %q", dStr)
			}
			ap.action = "delay"
			ap.delayMs = d
		} else if act == "crash" || act == "error:ENOSPC" || act == "error:EIO" {
			ap.action = act
		} else {
			return fmt.Errorf("unknown failpoint action: %q", act)
		}

		points[name] = ap
	}
	return nil
}

// Disarm clears all armed failpoints.
func Disarm() {
	mu.Lock()
	defer mu.Unlock()
	points = make(map[string]*armedPoint)
}

// Counters returns a copy of fired counts per failpoint.
func Counters() map[string]uint64 {
	mu.Lock()
	defer mu.Unlock()
	res := make(map[string]uint64, len(counters))
	for k, v := range counters {
		res[k] = v
	}
	return res
}

// Hit checks if a failpoint should fire and executes or returns the action.
func Hit(name string) Action {
	mu.Lock()
	ap, ok := points[name]
	if !ok {
		mu.Unlock()
		return Action{Kind: "none"}
	}

	ap.hits++
	shouldFire := false

	if ap.spec.AfterHits > 0 {
		if ap.hits == ap.spec.AfterHits {
			shouldFire = true
		}
	} else if ap.spec.Probability > 0 {
		if ap.rng.Float64() <= ap.spec.Probability {
			shouldFire = true
		}
	} else {
		// Default: fire immediately
		shouldFire = true
	}

	if !shouldFire {
		mu.Unlock()
		return Action{Kind: "none"}
	}

	ap.fired++
	counters[name]++

	action := Action{Kind: ap.action}
	if ap.action == "partial" {
		action.PartialBytes = ap.partSize
	} else if ap.action == "delay" {
		action.DelayMs = ap.delayMs
	} else if ap.action == "error:ENOSPC" {
		action.Err = ErrInjectedENOSPC
	} else if ap.action == "error:EIO" {
		action.Err = ErrInjectedEIO
	}

	if ap.once {
		delete(points, name)
	}
	mu.Unlock()

	// Execute immediate actions
	if action.Kind == "delay" {
		time.Sleep(time.Duration(action.DelayMs) * time.Millisecond)
		return Action{Kind: "none"}
	}
	if action.Kind == "crash" {
		SelfCrash()
	}

	return action
}

// MaybePartialWrite handles the partial:N action or standard writeFn.
func MaybePartialWrite(name string, buf []byte, writeFn func([]byte) (int, error)) (int, error) {
	act := Hit(name)
	if act.Kind == "error" || act.Err != nil {
		return 0, act.Err
	}
	if act.Kind == "partial" {
		n := act.PartialBytes
		if n > len(buf) {
			n = len(buf)
		}
		written, err := writeFn(buf[:n])
		// Immediately crash after writing partial bytes
		SelfCrash()
		return written, err
	}
	return writeFn(buf)
}

// SelfCrash abruptly terminates the process without running deferred cleanup.
func SelfCrash() {
	p, err := os.FindProcess(os.Getpid())
	if err == nil {
		_ = p.Kill()
	}
	os.Exit(137) // Fallback if kill returns
}

// RecordSyncedLength records the last fsynced length of a file to durability.ledger.
func RecordSyncedLength(filename string, length int64) {
	mu.Lock()
	dir := dataDir
	mu.Unlock()
	if dir == "" {
		return
	}
	ledgerPath := filepath.Join(dir, "durability.ledger")
	f, err := os.OpenFile(ledgerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	line := fmt.Sprintf("%s %d\n", filepath.Base(filename), length)
	_, _ = f.WriteString(line)
	_ = f.Sync()
}

