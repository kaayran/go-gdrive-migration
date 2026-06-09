package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"go-gdrive-migration/internal/config"
	"go-gdrive-migration/internal/pipeline"
)

var (
	version = "dev"
	commit  = "none"
)

// reportsBaseDir is used to place error reports next to the manifest file.
// Until config is loaded, falls back to the current directory.
var reportsBaseDir = "."

// noPause disables the "Press Enter to exit" prompt (for scripted runs).
var noPause bool

func main() {
	defer func() {
		if r := recover(); r != nil {
			details := fmt.Sprintf("panic: %v\n\nstack trace:\n%s", r, debug.Stack())
			fmt.Fprintf(os.Stderr, "\nfatal error (panic): %v\n", r)
			saveErrorReport(details)
			pauseBeforeExit()
			os.Exit(3)
		}
	}()

	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	subFolder := flag.String("sub-folder", "", "override sub_folder from config (path or list)")
	subFolderID := flag.String("sub-folder-id", "", "override sub_folder_id from config (id or list)")
	uploadFrom := flag.String("upload-from", "", "upload local folder to target_folder_id")
	targetSubfolderPostfix := flag.String("target-subfolder-postfix", "", "override options.target_subfolder_postfix from config")
	changeColor := flag.String("change-color", "", "set source sub-folder final color after copy (e.g. green, blue, #00ff00)")
	yes := flag.Bool("yes", false, "skip confirmation prompt")
	dryRun := flag.Bool("dry-run", false, "scan and show plan without copying")
	estimate := flag.Bool("estimate", false, "estimate source size/files and exit (no copy)")
	noResume := flag.Bool("no-resume", false, "ignore existing manifest, start from scratch")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(&noPause, "no-pause", false, "do not wait for Enter before exiting on error")
	flag.Parse()

	if *showVersion {
		fmt.Printf("go-gdrive-migration %s (%s)\n", version, commit)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(2, "config error: %v", err)
	}
	reportsBaseDir = filepath.Dir(cfg.Options.ManifestFile)

	// CLI flags override config values.
	// Priority: sub-folder-id > sub-folder > config values.
	if *subFolder != "" {
		cfg.SubFolder = *subFolder
		cfg.SubFolderID = ""
	}
	if *subFolderID != "" {
		cfg.SubFolderID = *subFolderID
		cfg.SubFolder = ""
	}
	if *uploadFrom != "" {
		cfg.Options.Mode = config.ModeLocalUpload
		cfg.SourceLocalPath = *uploadFrom
	}
	if *targetSubfolderPostfix != "" {
		cfg.Options.TargetSubfolderPostfix = *targetSubfolderPostfix
	}
	if *changeColor != "" {
		cfg.Options.ChangeColor = *changeColor
	}
	if *yes {
		cfg.Options.AssumeYes = true
	}
	if *dryRun {
		cfg.Options.DryRun = true
	}
	if *estimate {
		cfg.Options.EstimateOnly = true
	}
	if *noResume {
		cfg.Options.Resume = false
	}
	if err := cfg.Validate(); err != nil {
		fail(2, "config error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown on Ctrl+C to flush/close manifest cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\n→ Interrupt received, shutting down...")
		cancel()
	}()

	if err := pipeline.Run(ctx, cfg); err != nil {
		fail(1, "error: %v", err)
	}
}

// fail prints the error, writes an error report into reports/ and, when the
// program runs in an interactive console (e.g. started by double-click on
// Windows), waits for Enter so the window does not close before the error
// can be read.
func fail(exitCode int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "\n%s\n", msg)
	saveErrorReport(msg)
	pauseBeforeExit()
	os.Exit(exitCode)
}

// saveErrorReport writes the error into reports/error-<timestamp>.txt
// (next to the manifest file) so it can be inspected later.
func saveErrorReport(details string) {
	baseDir := reportsBaseDir
	if baseDir == "" {
		baseDir = "."
	}
	reportsDir := filepath.Join(baseDir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "→ Warning: cannot create reports dir: %v\n", err)
		return
	}
	ts := time.Now().Format("20060102-150405")
	path := filepath.Join(reportsDir, fmt.Sprintf("error-%s.txt", ts))
	body := fmt.Sprintf(
		"go-gdrive-migration error report\ntime: %s\nversion: %s (%s)\nargs: %v\n\n%s\n",
		time.Now().Format(time.RFC3339), version, commit, os.Args[1:], details,
	)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "→ Warning: cannot write error report: %v\n", err)
		return
	}
	fmt.Fprintf(os.Stderr, "→ Error details saved to: %s\n", path)
}

// pauseBeforeExit keeps the console window open until the user presses Enter.
// Skipped when -no-pause is set or stdin is not an interactive console
// (so scripts and CI runs never hang).
func pauseBeforeExit() {
	if noPause {
		return
	}
	fi, err := os.Stdin.Stat()
	if err != nil || (fi.Mode()&os.ModeCharDevice) == 0 {
		return
	}
	fmt.Fprint(os.Stderr, "\nPress Enter to exit...")
	var b [1]byte
	for {
		n, err := os.Stdin.Read(b[:])
		if err != nil || (n > 0 && b[0] == '\n') {
			return
		}
	}
}
