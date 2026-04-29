package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go-gdrive-migration/internal/config"
	"go-gdrive-migration/internal/pipeline"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	subFolder := flag.String("sub-folder", "", "override sub_folder from config (path or list)")
	subFolderID := flag.String("sub-folder-id", "", "override sub_folder_id from config (id or list)")
	targetSubfolderPostfix := flag.String("target-subfolder-postfix", "", "override options.target_subfolder_postfix from config")
	changeColor := flag.String("change-color", "", "set source sub-folder final color after copy (e.g. green, blue, #00ff00)")
	yes := flag.Bool("yes", false, "skip confirmation prompt")
	dryRun := flag.Bool("dry-run", false, "scan and show plan without copying")
	estimate := flag.Bool("estimate", false, "estimate source size/files and exit (no copy)")
	noResume := flag.Bool("no-resume", false, "ignore existing manifest, start from scratch")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("go-gdrive-migration %s (%s)\n", version, commit)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}

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
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}
