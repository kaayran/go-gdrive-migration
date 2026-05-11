package pipeline

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"strings"
	"time"

	"go-gdrive-migration/internal/auth"
	"go-gdrive-migration/internal/config"
	"go-gdrive-migration/internal/drive"
	"go-gdrive-migration/internal/manifest"
	"go-gdrive-migration/internal/progress"
)

// Run executes the full migration pipeline.
func Run(ctx context.Context, cfg *config.Config) error {
	colorizer, err := newSourceFolderColorizer(cfg.Options.ChangeColor)
	if err != nil {
		return fmt.Errorf("options.change_color: %w", err)
	}

	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Println(" go-gdrive-migration")
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf(" Mode             : %s\n", cfg.Options.Mode)
	if cfg.Options.IsLocalUpload() {
		fmt.Printf(" Source local path: %s\n", cfg.SourceLocalPath)
	} else {
		fmt.Printf(" Source folder ID : %s\n", cfg.SourceFolderID)
	}
	fmt.Printf(" Target folder ID : %s\n", cfg.TargetFolderID)
	subFolderIDs := cfg.SubFolderIDs()
	subFolders := cfg.SubFolders()
	if cfg.Options.IsLocalUpload() {
		fmt.Printf(" Upload workers   : %d\n", cfg.Options.UploadWorkers)
	} else if len(subFolderIDs) > 0 {
		fmt.Printf(" SubFolder IDs    : %d item(s)\n", len(subFolderIDs))
		for i, id := range subFolderIDs {
			fmt.Printf("   - [%d] %s\n", i+1, id)
		}
	} else if len(subFolders) > 1 {
		fmt.Printf(" SubFolders(path) : %d item(s)\n", len(subFolders))
		for i, p := range subFolders {
			fmt.Printf("   - [%d] %s\n", i+1, p)
		}
	} else if len(subFolders) == 1 {
		fmt.Printf(" SubFolder (path) : %s\n", subFolders[0])
	} else {
		fmt.Println(" Copy source root : true")
	}
	if !cfg.Options.IsLocalUpload() {
		fmt.Printf(" Workers          : scan=%d copy=%d\n", cfg.Options.ScanWorkers, cfg.Options.CopyWorkers)
	}
	fmt.Printf(" Manifest         : %s\n", cfg.Options.ManifestFile)
	fmt.Printf(" Resume           : %v\n", cfg.Options.Resume)
	fmt.Printf(" Dry run          : %v\n", cfg.Options.DryRun)
	fmt.Printf(" Estimate only    : %v\n", cfg.Options.EstimateOnly)
	if colorizer.enabled {
		fmt.Printf(" Source color     : in-progress=%s, done=%s\n", colorizer.inProgressLabel, colorizer.doneLabel)
	}
	fmt.Printf(" Target postfix   : %q\n", cfg.Options.TargetSubfolderPostfix)
	fmt.Printf(" Pre-flight scan  : %v\n", cfg.Options.IsScanEnabled())
	fmt.Println()

	reporter, err := newRunReportWriter(cfg.Options.ManifestFile)
	if err != nil {
		fmt.Printf(" Report file      : disabled (%v)\n", err)
	} else {
		fmt.Printf(" Report file      : %s\n", reporter.Path())
	}
	fmt.Println()

	// ── STAGE 0: AUTH ────────────────────────────────────────────────────
	fmt.Println("[1/6] Authenticating...")
	httpClient, err := auth.LoadClient(ctx, cfg.Auth.CredentialsFile, cfg.Auth.TokenFile)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	client, err := drive.NewClient(ctx, httpClient,
		cfg.Options.Retry.MaxAttempts,
		cfg.Options.Retry.InitialBackoffMs,
		cfg.Options.Retry.MaxBackoffMs,
	)
	if err != nil {
		return err
	}
	fmt.Println("      ✓ OK")
	fmt.Println()

	if cfg.Options.IsLocalUpload() {
		return runLocalUpload(ctx, cfg, client, reporter)
	}

	if len(subFolderIDs) == 0 {
		if len(subFolders) == 0 {
			return runSingleSubFolder(ctx, cfg, client, "", "", 1, 1, cfg.Options.Resume, reporter, colorizer)
		}
		for i, path := range subFolders {
			if len(subFolders) > 1 {
				fmt.Printf("──────────────────── Job %d/%d ────────────────────\n", i+1, len(subFolders))
				fmt.Printf("Sub-folder path: %s\n\n", path)
			}
			resumeForJob := cfg.Options.Resume || i > 0
			if err := runSingleSubFolder(ctx, cfg, client, "", path, i+1, len(subFolders), resumeForJob, reporter, colorizer); err != nil {
				return err
			}
		}
		return nil
	}
	for i, subID := range subFolderIDs {
		if len(subFolderIDs) > 1 {
			fmt.Printf("──────────────────── Job %d/%d ────────────────────\n", i+1, len(subFolderIDs))
			fmt.Printf("Sub-folder ID: %s\n\n", subID)
		}
		resumeForJob := cfg.Options.Resume || i > 0
		if err := runSingleSubFolder(ctx, cfg, client, subID, "", i+1, len(subFolderIDs), resumeForJob, reporter, colorizer); err != nil {
			return err
		}
	}
	return nil
}

func runSingleSubFolder(
	ctx context.Context,
	cfg *config.Config,
	client *drive.Client,
	subFolderID string,
	subFolderPath string,
	jobNumber int,
	jobTotal int,
	resumeForJob bool,
	reporter *runReportWriter,
	colorizer sourceFolderColorizer,
) error {
	fmt.Println("[2/6] Resolving source sub-folder...")
	subID, err := client.ResolveSubFolder(ctx, cfg.SourceFolderID, subFolderID, subFolderPath)
	if err != nil {
		return fmt.Errorf("resolve sub_folder: %w", err)
	}
	subInfo, err := client.GetFile(ctx, subID)
	if err != nil {
		return fmt.Errorf("get sub_folder info: %w", err)
	}
	fmt.Printf("      ✓ %q  (id: %s)\n", subInfo.Name, subID)
	targetSubName := subInfo.Name + cfg.Options.TargetSubfolderPostfix
	if targetSubName != subInfo.Name {
		fmt.Printf("      ✓ target sub-folder name: %q\n", targetSubName)
	}
	fmt.Println()

	if cfg.Options.EstimateOnly {
		return runEstimateOnly(ctx, cfg, client, subID, subInfo.Name)
	}

	requested := subFolderPath
	if subFolderID != "" {
		requested = subFolderID
	}

	if !cfg.Options.IsScanEnabled() {
		return runDirectCopyWithoutScan(ctx, cfg, client, subID, subInfo.Name, targetSubName, requested, resumeForJob, reporter, colorizer)
	}

	// ── STAGE 2: SCAN ────────────────────────────────────────────────────
	fmt.Println("[3/6] Scanning source tree...")
	scanner := drive.NewScanner(client, cfg.Options.ScanWorkers, cfg.Options.ListPageSize, cfg.Options.VerifyChecksums)
	scanStart := time.Now()
	scanSpinner := progress.NewSimpleSpinner("Scanning")
	scanner.OnProgress(func(p drive.ScanProgress) {
		scanSpinner.Tick(fmt.Sprintf("Scanning: %d folders, %d files, %s",
			p.Folders, p.Files, formatBytes(p.Bytes)))
	})
	scan, err := scanner.Scan(ctx, subID, subInfo.Name)
	scanSpinner.Finish()
	if err != nil {
		return fmt.Errorf("scan: %w", err)
	}
	emptyFolders := scan.EmptyFoldersCount()
	totalFiles := len(scan.Files)
	fmt.Printf("      ✓ Folders: %d  (empty: %d)  Files: %d  Size: %s  in %s\n",
		len(scan.Folders), emptyFolders, totalFiles,
		formatBytes(scan.TotalBytes), time.Since(scanStart).Round(time.Second))
	fmt.Println()

	if totalFiles == 0 {
		fmt.Println("Nothing to copy. Exit.")
		return nil
	}

	// ── STAGE 3: PRE-FLIGHT ──────────────────────────────────────────────
	fmt.Println("Plan summary:")
	fmt.Printf("  Will copy:    %d files  (%s)\n", totalFiles, formatBytes(scan.TotalBytes))
	if cfg.Options.SkipEmptyFolders {
		fmt.Printf("  Will create:  up to %d folders  (%d empty will be skipped)\n",
			len(scan.Folders)-emptyFolders-1, emptyFolders) // -1 = root already exists
	} else {
		fmt.Printf("  Will create:  %d folders\n", len(scan.Folders)-1)
	}
	fmt.Printf("  Mode:         server-side copy\n")
	fmt.Printf("  Manifest:     %s\n", cfg.Options.ManifestFile)
	fmt.Println()

	if cfg.Options.DryRun {
		fmt.Println("Dry run — exiting before any modifications.")
		return nil
	}

	if !cfg.Options.AssumeYes {
		fmt.Print("Continue? [Y/n]: ")
		if !confirm() {
			fmt.Println("Aborted by user.")
			return nil
		}
	}
	fmt.Println()

	// ── STAGE 4: MANIFEST + RESUME ───────────────────────────────────────
	mfst, err := manifest.Open(cfg.Options.ManifestFile, resumeForJob, subID)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer mfst.Close()
	if completed := mfst.CompletedCount(); completed > 0 && resumeForJob {
		fmt.Printf("→ Resume: %d files already copied, will be skipped.\n\n", completed)
	}

	// ── STAGE 5: TARGET SUB-FOLDER (get-or-create) ───────────────────────
	fmt.Println("[4/6] Preparing target sub-folder...")
	targetRootID, wasCreated, err := resolveOrCreateTargetRoot(ctx, client, mfst, cfg.TargetFolderID, targetSubName, subID)
	if err != nil {
		return fmt.Errorf("target sub-folder: %w", err)
	}
	if wasCreated {
		fmt.Printf("      ✓ Created %q  (id: %s)\n", targetSubName, targetRootID)
	} else {
		fmt.Printf("      ✓ Reusing %q  (id: %s)\n", targetSubName, targetRootID)
	}
	fmt.Println()

	// ── STAGE 6: PLAN — CREATE FOLDERS ───────────────────────────────────
	fmt.Println("[5/6] Creating target folder structure...")
	planner := drive.NewPlanner(client)
	planBar := (*progress.PlanBar)(nil)
	mapping, _, err := planner.CreateFolders(
		ctx, scan, targetRootID,
		cfg.Options.SkipEmptyFolders,
		func(created, total int) {
			if planBar == nil && total > 0 {
				planBar = progress.NewPlanBar(total, "Folders")
			}
			if planBar != nil {
				planBar.Set(created)
			}
		},
	)
	if planBar != nil {
		planBar.Finish()
	}
	if err != nil {
		return fmt.Errorf("create folders: %w", err)
	}
	fmt.Println()

	// ── STAGE 7: COPY ────────────────────────────────────────────────────
	fmt.Println("[6/6] Copying files (server-side)...")
	if err := colorizer.SetInProgress(ctx, client, subID, subInfo.Name); err != nil {
		return err
	}
	tasks := make([]drive.CopyTask, 0, totalFiles)
	for _, f := range scan.Files {
		targetParent, ok := mapping[f.ParentID]
		if !ok {
			// Parent was skipped (empty folder), but if a file exists this should not happen.
			// Fallback: place the file into the target sub-folder root.
			targetParent = targetRootID
		}
		tasks = append(tasks, drive.CopyTask{File: f, TargetParentID: targetParent})
	}

	bars := progress.NewCopyBars(int64(len(tasks)), scan.TotalBytes)
	copier := drive.NewCopier(client, cfg.Options.CopyWorkers)
	var copiedFiles int64
	var copiedBytes int64
	var skippedFiles int64
	var failedFiles int64
	copier.OnProgress(func(p drive.CopyProgress) {
		bars.Update(p.FilesDone, p.BytesDone)
	})
	copier.OnResult(func(r drive.CopyResult) {
		entry := manifest.Entry{
			SrcID:  r.Task.File.ID,
			DstID:  r.NewID,
			Path:   r.Task.File.Path,
			Size:   r.Task.File.Size,
			SrcMD5: r.Task.File.MD5,
			DstMD5: r.NewMD5,
		}
		switch {
		case r.Skipped:
			entry.Status = manifest.StatusSkipped
			atomic.AddInt64(&skippedFiles, 1)
		case r.Error != nil:
			entry.Status = manifest.StatusFailed
			entry.Error = r.Error.Error()
			atomic.AddInt64(&failedFiles, 1)
		default:
			entry.Status = manifest.StatusDone
			atomic.AddInt64(&copiedFiles, 1)
			atomic.AddInt64(&copiedBytes, r.Task.File.Size)
		}
		_ = mfst.Append(entry)
	})

	copyStart := time.Now()
	copyErr := copier.Run(ctx, tasks, scan.TotalBytes, mfst.IsDone)
	bars.Finish()
	elapsed := time.Since(copyStart).Round(time.Second)
	report := copyReport{
		mode:            "pre-flight scan + copy",
		sourceName:      subInfo.Name,
		sourceID:        subID,
		targetName:      targetSubName,
		targetID:        targetRootID,
		requested:       requested,
		discoveredFiles: int64(totalFiles),
		discoveredBytes: scan.TotalBytes,
		copiedFiles:     atomic.LoadInt64(&copiedFiles),
		copiedBytes:     atomic.LoadInt64(&copiedBytes),
		skippedFiles:    atomic.LoadInt64(&skippedFiles),
		failedFiles:     atomic.LoadInt64(&failedFiles),
		elapsed:         elapsed,
	}
	printMiniReport(report, copyErr)
	appendReport(reporter, report, copyErr)
	if copyErr != nil {
		return copyErr
	}
	if err := colorizer.SetDone(ctx, client, subID, subInfo.Name); err != nil {
		return err
	}
	if jobTotal > 1 {
		fmt.Printf(" ✓ Job %d/%d completed\n", jobNumber, jobTotal)
		fmt.Println("──────────────────────────────────────────────────────────────")
	}
	return nil
}

func confirm() bool {
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}

type folderPair struct {
	srcID string
	dstID string
	path  string
}

type copyReport struct {
	mode            string
	sourceName      string
	sourceID        string
	targetName      string
	targetID        string
	requested       string
	discoveredFiles int64
	discoveredBytes int64
	copiedFiles     int64
	copiedBytes     int64
	skippedFiles    int64
	failedFiles     int64
	elapsed         time.Duration
}

func runDirectCopyWithoutScan(
	ctx context.Context,
	cfg *config.Config,
	client *drive.Client,
	subID string,
	sourceName string,
	targetName string,
	requested string,
	resumeForJob bool,
	reporter *runReportWriter,
	colorizer sourceFolderColorizer,
) error {
	fmt.Println("[3/6] Scanning source tree...")
	fmt.Println("      • Pre-flight scanning is disabled by options.skip_scan=true.")
	fmt.Println()

	if cfg.Options.DryRun {
		fmt.Println("Dry run + options.skip_scan=true — scan is disabled, copy is skipped.")
		return nil
	}

	if !cfg.Options.AssumeYes {
		fmt.Print("Continue? [Y/n]: ")
		if !confirm() {
			fmt.Println("Aborted by user.")
			return nil
		}
	}
	fmt.Println()

	if cfg.Options.SkipEmptyFolders {
		fmt.Println("→ options.skip_empty_folders is ignored when options.skip_scan=true.")
		fmt.Println("  Empty folders can be created in direct copy mode.")
		fmt.Println()
	}

	mfst, err := manifest.Open(cfg.Options.ManifestFile, resumeForJob, subID)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer mfst.Close()
	if completed := mfst.CompletedCount(); completed > 0 && resumeForJob {
		fmt.Printf("→ Resume: %d files already copied, will be skipped.\n\n", completed)
	}

	fmt.Println("[4/6] Preparing target sub-folder...")
	targetRootID, wasCreated, err := resolveOrCreateTargetRoot(ctx, client, mfst, cfg.TargetFolderID, targetName, subID)
	if err != nil {
		return fmt.Errorf("target sub-folder: %w", err)
	}
	if wasCreated {
		fmt.Printf("      ✓ Created %q  (id: %s)\n", targetName, targetRootID)
	} else {
		fmt.Printf("      ✓ Reusing %q  (id: %s)\n", targetName, targetRootID)
	}
	fmt.Println()

	fmt.Println("[5/6] Creating target folder structure...")
	fmt.Println("      • On-the-fly during traversal (without pre-built plan).")
	fmt.Println()

	fmt.Println("[6/6] Copying files (server-side)...")
	copier := drive.NewCopier(client, cfg.Options.CopyWorkers)
	var copiedFiles int64
	var copiedBytes int64
	var skippedFiles int64
	var failedFiles int64
	copier.OnResult(func(r drive.CopyResult) {
		entry := manifest.Entry{
			SrcID:  r.Task.File.ID,
			DstID:  r.NewID,
			Path:   r.Task.File.Path,
			Size:   r.Task.File.Size,
			SrcMD5: r.Task.File.MD5,
			DstMD5: r.NewMD5,
		}
		switch {
		case r.Skipped:
			entry.Status = manifest.StatusSkipped
			atomic.AddInt64(&skippedFiles, 1)
		case r.Error != nil:
			entry.Status = manifest.StatusFailed
			entry.Error = r.Error.Error()
			atomic.AddInt64(&failedFiles, 1)
		default:
			entry.Status = manifest.StatusDone
			atomic.AddInt64(&copiedFiles, 1)
			atomic.AddInt64(&copiedBytes, r.Task.File.Size)
		}
		_ = mfst.Append(entry)
	})

	spinner := progress.NewSimpleSpinner("Direct copy")
	copyStart := time.Now()
	queue := []folderPair{{srcID: subID, dstID: targetRootID, path: sourceName}}
	discoveredFiles := int64(0)
	discoveredBytes := int64(0)
	colorMarked := false

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			spinner.Finish()
			return err
		}

		current := queue[0]
		queue = queue[1:]

		children, err := client.ListChildren(ctx, current.srcID, int64(cfg.Options.ListPageSize), cfg.Options.VerifyChecksums)
		if err != nil {
			spinner.Finish()
			return fmt.Errorf("list %q: %w", current.path, err)
		}

		tasks := make([]drive.CopyTask, 0, len(children))
		batchBytes := int64(0)
		for _, ch := range children {
			childPath := current.path + "/" + ch.Name
			if drive.IsFolder(ch) {
				newID, err := client.CreateFolder(ctx, current.dstID, ch.Name)
				if err != nil {
					spinner.Finish()
					return fmt.Errorf("create folder %q: %w", childPath, err)
				}
				queue = append(queue, folderPair{
					srcID: ch.Id,
					dstID: newID,
					path:  childPath,
				})
				continue
			}

			f := &drive.FileNode{
				ID:       ch.Id,
				Name:     ch.Name,
				Size:     ch.Size,
				MD5:      ch.Md5Checksum,
				ParentID: current.srcID,
				Path:     childPath,
			}
			discoveredFiles++
			discoveredBytes += ch.Size
			batchBytes += ch.Size
			tasks = append(tasks, drive.CopyTask{File: f, TargetParentID: current.dstID})
		}

		if len(tasks) > 0 {
			if !colorMarked {
				if err := colorizer.SetInProgress(ctx, client, subID, sourceName); err != nil {
					spinner.Finish()
					return err
				}
				colorMarked = true
			}
			if err := copier.Run(ctx, tasks, batchBytes, mfst.IsDone); err != nil {
				spinner.Finish()
				report := copyReport{
					mode:            "direct-copy (skip_scan=true)",
					sourceName:      sourceName,
					sourceID:        subID,
					targetName:      targetName,
					targetID:        targetRootID,
					requested:       requested,
					discoveredFiles: discoveredFiles,
					discoveredBytes: discoveredBytes,
					copiedFiles:     atomic.LoadInt64(&copiedFiles),
					copiedBytes:     atomic.LoadInt64(&copiedBytes),
					skippedFiles:    atomic.LoadInt64(&skippedFiles),
					failedFiles:     atomic.LoadInt64(&failedFiles),
					elapsed:         time.Since(copyStart).Round(time.Second),
				}
				printMiniReport(report, err)
				appendReport(reporter, report, err)
				return err
			}
		}

		spinner.Tick(fmt.Sprintf("Direct copy: %d files, %s", discoveredFiles, formatBytes(discoveredBytes)))
	}

	spinner.Finish()
	elapsed := time.Since(copyStart).Round(time.Second)
	if discoveredFiles == 0 {
		fmt.Println("Nothing to copy. Exit.")
		return nil
	}
	report := copyReport{
		mode:            "direct-copy (skip_scan=true)",
		sourceName:      sourceName,
		sourceID:        subID,
		targetName:      targetName,
		targetID:        targetRootID,
		requested:       requested,
		discoveredFiles: discoveredFiles,
		discoveredBytes: discoveredBytes,
		copiedFiles:     atomic.LoadInt64(&copiedFiles),
		copiedBytes:     atomic.LoadInt64(&copiedBytes),
		skippedFiles:    atomic.LoadInt64(&skippedFiles),
		failedFiles:     atomic.LoadInt64(&failedFiles),
		elapsed:         elapsed,
	}
	printMiniReport(report, nil)
	appendReport(reporter, report, nil)
	if colorMarked {
		if err := colorizer.SetDone(ctx, client, subID, sourceName); err != nil {
			return err
		}
	}
	return nil
}

type sourceFolderColorizer struct {
	enabled         bool
	inProgressHex   string
	inProgressLabel string
	doneHex         string
	doneLabel       string
}

func newSourceFolderColorizer(rawFinal string) (sourceFolderColorizer, error) {
	finalRaw := strings.TrimSpace(rawFinal)
	if finalRaw == "" {
		return sourceFolderColorizer{}, nil
	}

	inProgressHex, inProgressLabel, err := drive.ResolveFolderColor("yellow")
	if err != nil {
		return sourceFolderColorizer{}, err
	}
	doneHex, doneLabel, err := drive.ResolveFolderColor(finalRaw)
	if err != nil {
		return sourceFolderColorizer{}, err
	}
	return sourceFolderColorizer{
		enabled:         true,
		inProgressHex:   inProgressHex,
		inProgressLabel: inProgressLabel,
		doneHex:         doneHex,
		doneLabel:       doneLabel,
	}, nil
}

func (c sourceFolderColorizer) SetInProgress(ctx context.Context, client *drive.Client, folderID, folderName string) error {
	if !c.enabled {
		return nil
	}
	fmt.Printf("      • Source folder color: %q -> %s\n", folderName, c.inProgressLabel)
	if err := client.SetFolderColor(ctx, folderID, c.inProgressHex); err != nil {
		return fmt.Errorf("set source sub-folder color to %s: %w", c.inProgressLabel, err)
	}
	return nil
}

func (c sourceFolderColorizer) SetDone(ctx context.Context, client *drive.Client, folderID, folderName string) error {
	if !c.enabled {
		return nil
	}
	fmt.Printf("      • Source folder color: %q -> %s\n", folderName, c.doneLabel)
	if err := client.SetFolderColor(ctx, folderID, c.doneHex); err != nil {
		return fmt.Errorf("set source sub-folder color to %s: %w", c.doneLabel, err)
	}
	return nil
}

func printMiniReport(r copyReport, runErr error) {
	fmt.Println()
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Println(" Mini report")
	fmt.Println("──────────────────────────────────────────────────────────────")
	fmt.Printf(" Mode           : %s\n", r.mode)
	fmt.Printf(" Requested      : %s\n", r.requested)
	fmt.Printf(" Source         : %q (id: %s)\n", r.sourceName, r.sourceID)
	fmt.Printf(" Target         : %q (id: %s)\n", r.targetName, r.targetID)
	fmt.Printf(" Discovered     : %d files, %s\n", r.discoveredFiles, formatBytes(r.discoveredBytes))
	fmt.Printf(" Copied         : %d files, %s\n", r.copiedFiles, formatBytes(r.copiedBytes))
	fmt.Printf(" Skipped        : %d\n", r.skippedFiles)
	fmt.Printf(" Failed         : %d\n", r.failedFiles)
	fmt.Printf(" Elapsed        : %s\n", r.elapsed)
	if runErr != nil {
		fmt.Printf(" Result         : completed with errors (%v)\n", runErr)
		fmt.Println(" Notes          : see manifest.jsonl for failed/skipped details")
	} else {
		fmt.Println(" Result         : success")
	}
	fmt.Println("──────────────────────────────────────────────────────────────")
}

type runReportWriter struct {
	path string
}

func newRunReportWriter(manifestPath string) (*runReportWriter, error) {
	baseDir := filepath.Dir(manifestPath)
	if baseDir == "" || baseDir == "." {
		baseDir = "."
	}
	reportsDir := filepath.Join(baseDir, "reports")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create reports dir: %w", err)
	}
	ts := time.Now().Format("20060102-150405")
	path := filepath.Join(reportsDir, fmt.Sprintf("run-report-%s.txt", ts))
	header := fmt.Sprintf("go-gdrive-migration run report\ncreated_at: %s\n\n", time.Now().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
		return nil, fmt.Errorf("create run report file: %w", err)
	}
	return &runReportWriter{path: path}, nil
}

func (w *runReportWriter) Path() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *runReportWriter) Append(r copyReport, runErr error) error {
	if w == nil {
		return nil
	}
	result := "success"
	if runErr != nil {
		result = fmt.Sprintf("completed with errors (%v)", runErr)
	}
	body := fmt.Sprintf(
		"[job finished at %s]\nmode: %s\nrequested: %s\nsource: %q (id: %s)\ntarget: %q (id: %s)\ndiscovered: %d files, %s\ncopied: %d files, %s\nskipped: %d\nfailed: %d\nelapsed: %s\nresult: %s\n\n",
		time.Now().Format(time.RFC3339),
		r.mode,
		r.requested,
		r.sourceName, r.sourceID,
		r.targetName, r.targetID,
		r.discoveredFiles, formatBytes(r.discoveredBytes),
		r.copiedFiles, formatBytes(r.copiedBytes),
		r.skippedFiles,
		r.failedFiles,
		r.elapsed,
		result,
	)
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(body)
	return err
}

func appendReport(w *runReportWriter, r copyReport, runErr error) {
	if w == nil {
		return
	}
	if err := w.Append(r, runErr); err != nil {
		fmt.Printf("→ Warning: cannot append run report: %v\n", err)
	}
}

func runEstimateOnly(
	ctx context.Context,
	cfg *config.Config,
	client *drive.Client,
	subID string,
	subName string,
) error {
	fmt.Println("[3/6] Estimating source tree...")
	scanner := drive.NewScanner(client, cfg.Options.ScanWorkers, cfg.Options.ListPageSize, false)
	start := time.Now()
	spinner := progress.NewSimpleSpinner("Estimating")
	scanner.OnProgress(func(p drive.ScanProgress) {
		spinner.Tick(fmt.Sprintf("Estimating: %d folders, %d files, %s",
			p.Folders, p.Files, formatBytes(p.Bytes)))
	})
	estimate, err := scanner.Estimate(ctx, subID)
	spinner.Finish()
	if err != nil {
		return fmt.Errorf("estimate: %w", err)
	}

	fmt.Printf("      ✓ %q\n", subName)
	fmt.Printf("      ✓ Folders: %d  Files: %d  Size: %s  in %s\n",
		estimate.Folders, estimate.Files, formatBytes(estimate.Bytes), time.Since(start).Round(time.Second))
	fmt.Println()
	fmt.Println("Estimate mode — exiting before any modifications.")
	return nil
}

// resolveOrCreateTargetRoot keeps target sub-folder creation idempotent.
//
// Logic:
//  1. If manifest has targetRoot ID from a previous run, verify it still exists
//     and reuse it.
//  2. Otherwise search by name in target_folder_id. If exactly one match exists,
//     reuse it (for cases where manifest was removed but the folder remained).
//     If multiple matches are found, return an error.
//  3. Otherwise create a new folder and persist its ID in manifest.
func resolveOrCreateTargetRoot(
	ctx context.Context,
	client *drive.Client,
	mfst *manifest.Manifest,
	targetParentID, name, srcSubID string,
) (string, bool, error) {

	// 1. From manifest (resume).
	if id := mfst.TargetRootID(); id != "" {
		if f, err := client.GetFile(ctx, id); err == nil && f != nil {
			return id, false, nil
		}
		// Folder from manifest was not found (deleted?) -> continue with search/create.
	}

	// 2. Search for existing folders with the same name in target.
	children, err := client.ListChildren(ctx, targetParentID, 1000, false)
	if err != nil {
		return "", false, fmt.Errorf("list target children: %w", err)
	}
	var matches []string
	for _, c := range children {
		if drive.IsFolder(c) && c.Name == name {
			matches = append(matches, c.Id)
		}
	}
	switch len(matches) {
	case 1:
		id := matches[0]
		_ = mfst.SetTargetRoot(srcSubID, id, name)
		return id, false, nil
	case 0:
		// 3. Create a new one.
		id, err := client.CreateFolder(ctx, targetParentID, name)
		if err != nil {
			return "", false, fmt.Errorf("create %q in target: %w", name, err)
		}
		if err := mfst.SetTargetRoot(srcSubID, id, name); err != nil {
			return "", false, fmt.Errorf("save target root to manifest: %w", err)
		}
		return id, true, nil
	default:
		return "", false, fmt.Errorf(
			"found %d folders named %q in target_folder_id — please remove duplicates manually or set sub_folder_id explicitly",
			len(matches), name)
	}
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
