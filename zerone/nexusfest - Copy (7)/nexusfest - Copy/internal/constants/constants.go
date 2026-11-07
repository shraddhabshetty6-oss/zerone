// Package constants defines canonical values per ARCHITECTURE.md Appendix A.
package constants

import "time"

const (
	// FormatVersion is the canonical database file format version.
	FormatVersion uint16 = 1

	// HeaderSize is the fixed size of the data file header in bytes.
	HeaderSize int64 = 4096

	// BlockSize is the size of one allocation block in bytes.
	BlockSize int64 = 4096

	// RecordSize is the fixed size of a student record slot in bytes.
	RecordSize int64 = 256

	// SlotsPerBlock is the number of 256-byte slots in a 4096-byte block.
	SlotsPerBlock uint32 = 16

	// RecordCRCOffset is the byte offset where CRC32 begins in a slot.
	RecordCRCOffset = 252

	// MaxOpsPerTx is the maximum number of operations permitted in a single transaction.
	MaxOpsPerTx = 1000

	// MaxSlotGrowthPerTx is the maximum number of slots by which a transaction may grow the file.
	MaxSlotGrowthPerTx uint32 = 1024

	// DataHeaderMagic is the 8-byte ASCII magic identifier for students.dat.
	DataHeaderMagic = "BSDBDATA"

	// RecordMagic is the 2-byte magic identifier for active/deleted record slots.
	RecordMagic uint16 = 0x5352

	// RecordFormat is the 1-byte format identifier for records.
	RecordFormat uint8 = 1

	// Record status flags.
	StatusFree    uint8 = 0
	StatusActive  uint8 = 1
	StatusDeleted uint8 = 2

	// WALHeaderMagic is the 4-byte ASCII magic identifier for students.wal.
	WALHeaderMagic = "BSWL"

	// WALHeaderSize is the size of the WAL header in bytes.
	WALHeaderSize int64 = 32

	// WALEntryMagic is the 4-byte ASCII magic identifier for each WAL entry.
	WALEntryMagic = "WALE"

	// WAL entry types.
	WalTypeBegin     uint8 = 1
	WalTypeWriteSlot uint8 = 2
	WalTypeCommit    uint8 = 3

	// IndexHeaderMagic is the 4-byte ASCII magic identifier for students.idx.
	IndexHeaderMagic = "BSIX"

	// IndexVersion is the version of the persisted index snapshot.
	IndexVersion uint16 = 1

	// DefaultWALMaxBytes is the threshold (4 MiB) to trigger a checkpoint.
	DefaultWALMaxBytes int64 = 4 * 1024 * 1024

	// DefaultTxLockTimeout is the timeout for acquiring the writer transaction lock.
	DefaultTxLockTimeout = 5 * time.Second

	// DefaultSyncMode is the default sync mode ("always").
	DefaultSyncMode = "always"

	// DefaultStartupVerify is the default startup verification mode ("full").
	DefaultStartupVerify = "full"

	// DefaultOnCorruption is the default policy on encountering corruption ("quarantine").
	DefaultOnCorruption = "quarantine"

	// ScanChunkSlots is the chunk size in slots for sequential scans (64 KiB = 256 slots).
	ScanChunkSlots uint32 = 256

	// MaxPageLimit is the maximum records returned in a single scan or range query page.
	MaxPageLimit = 1000

	// Exit codes per ARCHITECTURE.md §B.4 and §E.10.
	ExitSuccess       = 0
	ExitUsage         = 1
	ExitVerifyFailed  = 2
	ExitRecoveryFatal = 3
	ExitLocked        = 4
)

