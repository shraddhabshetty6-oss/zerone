// Package faultinject provides failpoint hooks for crash and error simulation per ARCHITECTURE.md §H.4.
package faultinject

import "errors"

// Known failpoint names from the normative catalog in ARCHITECTURE.md §H.4.
const (
	FPWalAppendBeforeWrite         = "wal.append.before_write"
	FPWalAppendPartialWrite        = "wal.append.partial_write"
	FPWalAppendAfterWriteBeforeSync = "wal.append.after_write_before_sync"
	FPWalCommitAfterSync           = "wal.commit.after_sync"
	FPDataApplyBeforeSlotWrite     = "data.apply.before_slot_write"
	FPDataApplyPartialSlotWrite    = "data.apply.partial_slot_write"
	FPDataApplyAfterSlotWrite      = "data.apply.after_slot_write"
	FPDataExtendPartial            = "data.extend.partial"
	FPDataSyncError                = "data.sync.error"
	FPDiskEnospcWal                = "disk.enospc.wal"
	FPDiskEnospcData               = "disk.enospc.data"
	FPDiskEnospcCheckpoint         = "disk.enospc.checkpoint"
	FPCheckpointBeforeDataSync     = "checkpoint.before_data_sync"
	FPCheckpointAfterIndexTmpWrite = "checkpoint.after_index_tmp_write"
	FPCheckpointAfterIndexRename   = "checkpoint.after_index_rename"
	FPCheckpointBeforeWalRename    = "checkpoint.before_wal_rename"
	FPCheckpointAfterWalRename     = "checkpoint.after_wal_rename"
	FPRecoveryMidRedo              = "recovery.mid_redo"
	FPRecoveryBeforeCheckpoint     = "recovery.before_checkpoint"
)

// Catalog of all valid failpoint names.
var Catalog = map[string]struct{}{
	FPWalAppendBeforeWrite:         {},
	FPWalAppendPartialWrite:        {},
	FPWalAppendAfterWriteBeforeSync: {},
	FPWalCommitAfterSync:           {},
	FPDataApplyBeforeSlotWrite:     {},
	FPDataApplyPartialSlotWrite:    {},
	FPDataApplyAfterSlotWrite:      {},
	FPDataExtendPartial:            {},
	FPDataSyncError:                {},
	FPDiskEnospcWal:                {},
	FPDiskEnospcData:               {},
	FPDiskEnospcCheckpoint:         {},
	FPCheckpointBeforeDataSync:     {},
	FPCheckpointAfterIndexTmpWrite: {},
	FPCheckpointAfterIndexRename:   {},
	FPCheckpointBeforeWalRename:    {},
	FPCheckpointAfterWalRename:     {},
	FPRecoveryMidRedo:              {},
	FPRecoveryBeforeCheckpoint:     {},
}

// Sentinel errors injected by failpoints.
var (
	ErrInjectedENOSPC = errors.New("injected error: no space left on device (ENOSPC)")
	ErrInjectedEIO    = errors.New("injected error: input/output error (EIO)")
)

// Action describes the action taken when a failpoint fires.
type Action struct {
	Kind         string // "none", "crash", "partial", "error", "delay"
	PartialBytes int
	Err          error
	DelayMs      int
}

// FailpointSpec describes the configuration for arming a failpoint.
type FailpointSpec struct {
	Action      string  `json:"action"`                // "crash", "partial:N", "error:ENOSPC", "error:EIO", "delay:ms"
	AfterHits   int     `json:"after_hits,omitempty"`  // Fire on the N-th hit
	Probability float64 `json:"probability,omitempty"` // Probability [0.0, 1.0]
	Seed        int64   `json:"seed,omitempty"`        // Random seed
	Once        *bool   `json:"once,omitempty"`        // Fire at most once (default true for crash)
}

