// BinStudentDB CLI entry point (GO-16) per ARCHITECTURE.md §E.10.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"studentdb/internal/buildinfo"
	"studentdb/internal/concurrency"
	"studentdb/internal/constants"
	"studentdb/internal/engine"
	"studentdb/internal/faultinject"
	"studentdb/internal/httpapi"
	"studentdb/internal/index"
	"studentdb/internal/record"
	"studentdb/internal/recovery"
	"studentdb/internal/retrieval"
	"studentdb/internal/storage"
	"studentdb/internal/verify"
	"studentdb/internal/wal"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(constants.ExitUsage)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version":
		handleVersion(args)
	case "init":
		handleInit(args)
	case "import":
		handleImport(args)
	case "serve":
		handleServe(args)
	case "verify":
		handleVerify(args)
	case "recover":
		handleRecover(args)
	case "checkpoint":
		handleCheckpoint(args)
	case "stats":
		handleStats(args)
	case "dump":
		handleDump(args)
	case "backup":
		handleBackup(args)
	case "-h", "--help", "help":
		printUsage()
		os.Exit(constants.ExitSuccess)
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n\n", cmd)
		printUsage()
		os.Exit(constants.ExitUsage)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `BinStudentDB — Go Binary Student Database (v1.0.0, build: %s)

Usage:
  studentdb <command> [flags]

Commands:
  init        Create empty database files in --data-dir
  import      Import records from CSV file in batches of 100
  serve       Run recovery then serve loopback HTTP API
  verify      Perform offline consistency validation (exit 2 on failure)
  recover     Run startup recovery and output recovery report
  checkpoint  Run recovery, force checkpoint, and exit
  stats       Print database counts and stats
  dump        Sequential dump of active records (json or csv)
  backup      Create verified cold backup with SHA-256 manifest
  version     Display version and build capability
`, buildinfo.Build)
}

func handleVersion(args []string) {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "Output version as JSON")
	_ = fs.Parse(args)

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
			"version":        "1.0.0",
			"format_version": constants.FormatVersion,
			"build":          buildinfo.Build,
		})
	} else {
		fmt.Printf("BinStudentDB v1.0.0 (format v%d, build %s)\n", constants.FormatVersion, buildinfo.Build)
	}
}

func handleInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Target database directory")
	_ = fs.Parse(args)

	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(constants.ExitUsage)
	}

	// Create students.dat with 4096-byte header
	if err := storage.Create(*dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing data file: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	// Create empty students.wal with header
	if err := wal.Create(*dataDir, 0, 1); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing WAL: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	// Create empty students.idx
	idx := index.New()
	idxPath := filepath.Join(*dataDir, "students.idx")
	if err := idx.SaveSnapshot(idxPath, 0, 0); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing index: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	// Create students.lock file
	lockPath := filepath.Join(*dataDir, "students.lock")
	if f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600); err == nil {
		_ = f.Close()
	}

	fmt.Printf("Database initialized successfully in %s\n", *dataDir)
}

func handleServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	addr := fs.String("addr", "127.0.0.1:8080", "Listen address (loopback only)")
	syncMode := fs.String("sync-mode", constants.DefaultSyncMode, "fsync mode: always|none")
	startupVerify := fs.String("startup-verify", constants.DefaultStartupVerify, "verify mode: full|fast")
	onCorruption := fs.String("on-corruption", constants.DefaultOnCorruption, "corruption mode: quarantine|fail")
	walMaxBytes := fs.Int64("wal-max-bytes", constants.DefaultWALMaxBytes, "max WAL bytes before checkpoint")
	_ = fs.String("log-level", "info", "Log level")
	_ = fs.Parse(args)

	faultinject.SetDataDir(*dataDir)

	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      *syncMode,
		StartupVerify: *startupVerify,
		OnCorruption:  *onCorruption,
		WalMaxBytes:   *walMaxBytes,
	}

	db, err := engine.Open(cfg)
	if err != nil {
		if errors.Is(err, concurrency.ErrLocked) {
			fmt.Fprintf(os.Stderr, "Error: database directory %s is locked\n", *dataDir)
			os.Exit(constants.ExitLocked)
		}
		fmt.Fprintf(os.Stderr, "Recovery failed: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	srv, err := httpapi.NewServer(db, *addr, *syncMode, *startupVerify)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Server initialization failed: %v\n", err)
		_ = db.Close()
		os.Exit(constants.ExitUsage)
	}

	boundAddr, err := srv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start listener: %v\n", err)
		_ = db.Close()
		os.Exit(constants.ExitUsage)
	}

	// Ready JSON line to stdout per ARCHITECTURE.md §B.4
	readyMsg := map[string]string{
		"event":           "ready",
		"addr":            boundAddr,
		"state":           db.State(),
		"recovery_report": db.ReportPath(),
	}
	readyBytes, _ := json.Marshal(readyMsg)
	fmt.Println(string(readyBytes))

	// Trap SIGTERM and SIGINT for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = srv.Shutdown(ctx)
	_ = db.Close()
	os.Exit(constants.ExitSuccess)
}

func handleVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	asJSON := fs.Bool("json", false, "Output verification findings as JSON")
	_ = fs.Parse(args)

	res, err := verify.VerifyOffline(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Verification I/O error: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		if res.OK {
			fmt.Println("Verification SUCCESS: all invariants satisfied.")
		} else {
			fmt.Fprintf(os.Stderr, "Verification FAILED with %d findings:\n", len(res.Findings))
			for _, f := range res.Findings {
				fmt.Fprintf(os.Stderr, "  [%s] Invariant %s: %s\n", f.Severity, f.Invariant, f.Detail)
			}
		}
	}

	if !res.OK {
		os.Exit(constants.ExitVerifyFailed)
	}
	os.Exit(constants.ExitSuccess)
}

func handleRecover(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	_ = fs.Parse(args)

	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      constants.DefaultSyncMode,
		StartupVerify: "full",
		OnCorruption:  "quarantine",
		WalMaxBytes:   constants.DefaultWALMaxBytes,
	}

	lockMgr := concurrency.NewLockManager(constants.DefaultTxLockTimeout)
	recState, err := recovery.Recover(cfg, lockMgr)
	if err != nil {
		if errors.Is(err, concurrency.ErrLocked) {
			os.Exit(constants.ExitLocked)
		}
		fmt.Fprintf(os.Stderr, "Recovery fatal error: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	_ = recState.Storage.Close()
	_ = recState.WAL.Close()
	_ = lockMgr.ReleaseProcessLock()

	reportData, _ := json.MarshalIndent(recState.Report, "", "  ")
	fmt.Println(string(reportData))
	os.Exit(constants.ExitSuccess)
}

func handleCheckpoint(args []string) {
	fs := flag.NewFlagSet("checkpoint", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	_ = fs.Parse(args)

	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      "always",
		StartupVerify: "full",
		OnCorruption:  "quarantine",
		WalMaxBytes:   constants.DefaultWALMaxBytes,
	}

	db, err := engine.Open(cfg)
	if err != nil {
		os.Exit(constants.ExitRecoveryFatal)
	}

	lsn, err := db.ForceCheckpoint()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Checkpoint failed: %v\n", err)
		_ = db.Close()
		os.Exit(constants.ExitRecoveryFatal)
	}

	_ = db.Close()
	fmt.Printf("Checkpoint succeeded at LSN %d\n", lsn)
	os.Exit(constants.ExitSuccess)
}

func handleStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	asJSON := fs.Bool("json", false, "Output stats as JSON")
	_ = fs.Parse(args)

	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      "always",
		StartupVerify: "fast",
		OnCorruption:  "quarantine",
		WalMaxBytes:   constants.DefaultWALMaxBytes,
	}

	db, err := engine.Open(cfg)
	if err != nil {
		os.Exit(constants.ExitRecoveryFatal)
	}
	defer db.Close()

	stats := db.Stats()
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(stats)
	} else {
		fmt.Printf("Active records:        %d\n", stats.ActiveCount)
		fmt.Printf("Total file slots:      %d\n", stats.SlotCount)
		fmt.Printf("WAL file size:         %d bytes\n", stats.WalSizeBytes)
		fmt.Printf("Last checkpoint LSN:   %d\n", stats.LastCheckpointLSN)
	}
}

func handleDump(args []string) {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	format := fs.String("format", "csv", "Output format: csv|json")
	_ = fs.Parse(args)

	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      "always",
		StartupVerify: "fast",
		OnCorruption:  "quarantine",
		WalMaxBytes:   constants.DefaultWALMaxBytes,
	}

	db, err := engine.Open(cfg)
	if err != nil {
		os.Exit(constants.ExitRecoveryFatal)
	}
	defer db.Close()

	var cursor uint32 = 0
	var allStudents []*record.Student

	for {
		page, err := db.SequentialScan(cursor, 1000, retrieval.Filter{}, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Dump scan error: %v\n", err)
			os.Exit(constants.ExitRecoveryFatal)
		}
		allStudents = append(allStudents, page.Items...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}

	if *format == "json" {
		_ = json.NewEncoder(os.Stdout).Encode(allStudents)
		return
	}

	// CSV format
	w := csv.NewWriter(os.Stdout)
	defer w.Flush()
	_ = w.Write([]string{
		"student_id", "first_name", "last_name", "date_of_birth", "gender",
		"email", "phone", "department", "enrollment_year", "gpa",
	})
	for _, s := range allStudents {
		_ = w.Write([]string{
			strconv.FormatUint(uint64(s.StudentID), 10),
			s.FirstName,
			s.LastName,
			s.DateOfBirth,
			s.Gender,
			s.Email,
			s.Phone,
			s.Department,
			strconv.FormatUint(uint64(s.EnrollmentYear), 10),
			fmt.Sprintf("%.2f", s.GPA),
		})
	}
}

func handleImport(args []string) {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Database directory")
	csvInput := fs.String("input", "", "Path to CSV file to import")
	_ = fs.Parse(args)

	if *csvInput == "" {
		fmt.Fprintln(os.Stderr, "Error: --input CSV file is required")
		os.Exit(constants.ExitUsage)
	}

	csvFile, err := os.Open(*csvInput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening CSV: %v\n", err)
		os.Exit(constants.ExitUsage)
	}
	defer csvFile.Close()

	reader := csv.NewReader(csvFile)
	header, err := reader.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading CSV header: %v\n", err)
		os.Exit(constants.ExitUsage)
	}
	_ = header

	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      "always",
		StartupVerify: "full",
		OnCorruption:  "quarantine",
		WalMaxBytes:   constants.DefaultWALMaxBytes,
	}

	db, err := engine.Open(cfg)
	if err != nil {
		os.Exit(constants.ExitRecoveryFatal)
	}
	defer db.Close()

	var batch []engine.BatchOp
	rowCount := 0

	commitBatch := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, _, err := db.Batch(context.Background(), batch)
		if err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading CSV row: %v\n", err)
			os.Exit(constants.ExitUsage)
		}

		if len(row) < 10 {
			fmt.Fprintf(os.Stderr, "Invalid row length at row %d\n", rowCount+1)
			os.Exit(constants.ExitUsage)
		}

		sid, err := strconv.ParseUint(row[0], 10, 32)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid student_id at row %d: %v\n", rowCount+1, err)
			os.Exit(constants.ExitUsage)
		}

		year, _ := strconv.ParseUint(row[8], 10, 16)
		gpa, _ := strconv.ParseFloat(row[9], 64)

		st := &record.Student{
			StudentID:      uint32(sid),
			FirstName:      row[1],
			LastName:       row[2],
			DateOfBirth:    row[3],
			Gender:         row[4],
			Email:          row[5],
			Phone:          row[6],
			Department:     row[7],
			EnrollmentYear: uint16(year),
			GPA:            gpa,
		}

		batch = append(batch, engine.BatchOp{
			Op:      "insert",
			Student: st,
		})
		rowCount++

		if len(batch) >= 100 {
			if err := commitBatch(); err != nil {
				fmt.Fprintf(os.Stderr, "Batch commit error: %v\n", err)
				os.Exit(constants.ExitRecoveryFatal)
			}
		}
	}

	if err := commitBatch(); err != nil {
		fmt.Fprintf(os.Stderr, "Final batch commit error: %v\n", err)
		os.Exit(constants.ExitRecoveryFatal)
	}

	// Final checkpoint
	_, _ = db.ForceCheckpoint()

	fmt.Printf("Successfully imported %d student records into %s\n", rowCount, *dataDir)
}

func handleBackup(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data-dir", "data/live", "Source database directory")
	destDir := fs.String("dest", "", "Destination backup directory")
	_ = fs.Parse(args)

	if *destDir == "" {
		fmt.Fprintln(os.Stderr, "Error: --dest directory is required")
		os.Exit(constants.ExitUsage)
	}

	// Ensure database is checkpointed
	cfg := recovery.Config{
		DataDir:       *dataDir,
		SyncMode:      "always",
		StartupVerify: "full",
		OnCorruption:  "fail",
		WalMaxBytes:   constants.DefaultWALMaxBytes,
	}

	db, err := engine.Open(cfg)
	if err != nil {
		os.Exit(constants.ExitRecoveryFatal)
	}
	_, _ = db.ForceCheckpoint()
	_ = db.Close()

	if err := os.MkdirAll(*destDir, 0700); err != nil {
		os.Exit(constants.ExitUsage)
	}

	// Copy data files
	filesToCopy := []string{"students.dat", "students.wal", "students.idx"}
	fileHashes := make(map[string]string)

	for _, fname := range filesToCopy {
		src := filepath.Join(*dataDir, fname)
		dst := filepath.Join(*destDir, fname)

		sF, err := os.Open(src)
		if err != nil {
			continue
		}
		dF, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			_ = sF.Close()
			os.Exit(constants.ExitRecoveryFatal)
		}

		hasher := sha256.New()
		multi := io.MultiWriter(dF, hasher)
		if _, err := io.Copy(multi, sF); err != nil {
			_ = sF.Close()
			_ = dF.Close()
			os.Exit(constants.ExitRecoveryFatal)
		}
		_ = dF.Sync()
		_ = sF.Close()
		_ = dF.Close()

		fileHashes[fname] = hex.EncodeToString(hasher.Sum(nil))
	}

	// Write manifest.json
	manifest := map[string]any{
		"created_at":  time.Now().UTC().Format(time.RFC3339),
		"source":      *dataDir,
		"files":       fileHashes,
		"format_ver":  constants.FormatVersion,
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(*destDir, "manifest.json"), manifestBytes, 0600)

	// Verify backup copy
	res, err := verify.VerifyOffline(*destDir)
	if err != nil || !res.OK {
		fmt.Fprintf(os.Stderr, "Backup verification failed: %v\n", err)
		os.Exit(constants.ExitVerifyFailed)
	}

	fmt.Printf("Cold backup created and verified in %s\n", *destDir)
}

