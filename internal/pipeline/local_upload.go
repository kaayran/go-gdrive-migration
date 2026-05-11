package pipeline

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go-gdrive-migration/internal/config"
	"go-gdrive-migration/internal/drive"
	"go-gdrive-migration/internal/manifest"
	"go-gdrive-migration/internal/progress"
)

type localFolderNode struct {
	ID         string
	Name       string
	ParentID   string
	Path       string
	TotalFiles int
}

type localScanResult struct {
	RootID     string
	RootName   string
	RootPath   string
	Folders    map[string]*localFolderNode
	Files      []*drive.LocalFileNode
	TotalBytes int64
}

func runLocalUpload(ctx context.Context, cfg *config.Config, client *drive.Client, reporter *runReportWriter) error {
	sourcePath := filepath.Clean(cfg.SourceLocalPath)
	rootName := filepath.Base(sourcePath)
	targetName := rootName + cfg.Options.TargetSubfolderPostfix

	fmt.Println("[2/6] Resolving local source folder...")
	fmt.Printf("      ✓ %q\n", sourcePath)
	if targetName != rootName {
		fmt.Printf("      ✓ target sub-folder name: %q\n", targetName)
	}
	fmt.Println()

	fmt.Println("[3/6] Scanning local tree...")
	scanStart := time.Now()
	scan, err := scanLocalFolder(ctx, sourcePath, rootName)
	if err != nil {
		return fmt.Errorf("scan local folder: %w", err)
	}
	emptyFolders := scan.emptyFoldersCount()
	fmt.Printf("      ✓ Folders: %d  (empty: %d)  Files: %d  Size: %s  in %s\n",
		len(scan.Folders), emptyFolders, len(scan.Files), formatBytes(scan.TotalBytes), time.Since(scanStart).Round(time.Second))
	fmt.Println()

	if cfg.Options.EstimateOnly {
		fmt.Println("Estimate mode — exiting before any modifications.")
		return nil
	}
	if len(scan.Files) == 0 && emptyFolders == len(scan.Folders) {
		fmt.Println("Nothing to upload. Exit.")
		return nil
	}

	foldersToCreate := len(scan.Folders) - 1
	if cfg.Options.SkipEmptyFolders {
		foldersToCreate = 0
		for id, f := range scan.Folders {
			if id != scan.RootID && f.TotalFiles > 0 {
				foldersToCreate++
			}
		}
	}

	fmt.Println("Plan summary:")
	fmt.Printf("  Will upload:  %d files  (%s)\n", len(scan.Files), formatBytes(scan.TotalBytes))
	fmt.Printf("  Will create:  %d folders\n", foldersToCreate)
	fmt.Printf("  Mode:         local upload\n")
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

	mfst, err := manifest.Open(cfg.Options.ManifestFile, cfg.Options.Resume, scan.RootID)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer mfst.Close()
	if completed := mfst.CompletedCount(); completed > 0 && cfg.Options.Resume {
		fmt.Printf("→ Resume: %d files already uploaded, will be skipped.\n\n", completed)
	}

	fmt.Println("[4/6] Preparing target sub-folder...")
	targetRootID, wasCreated, err := resolveOrCreateTargetRoot(ctx, client, mfst, cfg.TargetFolderID, targetName, scan.RootID)
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
	mapping, err := createLocalTargetFolders(ctx, client, scan, targetRootID, cfg.Options.SkipEmptyFolders)
	if err != nil {
		return fmt.Errorf("create folders: %w", err)
	}
	fmt.Println()

	fmt.Println("[6/6] Uploading files...")
	tasks := make([]drive.UploadTask, 0, len(scan.Files))
	for _, f := range scan.Files {
		targetParent, ok := mapping[f.ParentID]
		if !ok {
			targetParent = targetRootID
		}
		tasks = append(tasks, drive.UploadTask{File: f, TargetParentID: targetParent})
	}

	bars := progress.NewCopyBars(int64(len(tasks)), scan.TotalBytes)
	uploader := drive.NewUploader(client, cfg.Options.UploadWorkers)
	var uploadedFiles int64
	var uploadedBytes int64
	var skippedFiles int64
	var failedFiles int64
	uploader.OnProgress(func(p drive.CopyProgress) {
		bars.Update(p.FilesDone, p.BytesDone)
	})
	uploader.OnResult(func(r drive.UploadResult) {
		entry := manifest.Entry{
			SrcID: r.Task.File.ID,
			DstID: r.NewID,
			Path:  r.Task.File.Path,
			Size:  r.Task.File.Size,
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
			atomic.AddInt64(&uploadedFiles, 1)
			atomic.AddInt64(&uploadedBytes, r.Task.File.Size)
		}
		_ = mfst.Append(entry)
	})

	uploadStart := time.Now()
	uploadErr := uploader.Run(ctx, tasks, scan.TotalBytes, mfst.IsDone)
	bars.Finish()
	elapsed := time.Since(uploadStart).Round(time.Second)
	report := copyReport{
		mode:            "local upload",
		sourceName:      scan.RootName,
		sourceID:        scan.RootPath,
		targetName:      targetName,
		targetID:        targetRootID,
		requested:       scan.RootPath,
		discoveredFiles: int64(len(scan.Files)),
		discoveredBytes: scan.TotalBytes,
		copiedFiles:     atomic.LoadInt64(&uploadedFiles),
		copiedBytes:     atomic.LoadInt64(&uploadedBytes),
		skippedFiles:    atomic.LoadInt64(&skippedFiles),
		failedFiles:     atomic.LoadInt64(&failedFiles),
		elapsed:         elapsed,
	}
	printMiniReport(report, uploadErr)
	appendReport(reporter, report, uploadErr)
	return uploadErr
}

func scanLocalFolder(ctx context.Context, rootPath, rootName string) (*localScanResult, error) {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	rootID := "local-folder-root:" + filepath.ToSlash(filepath.Clean(absRoot))
	scan := &localScanResult{
		RootID:   rootID,
		RootName: rootName,
		RootPath: absRoot,
		Folders:  make(map[string]*localFolderNode),
	}
	scan.Folders[rootID] = &localFolderNode{
		ID:   rootID,
		Name: rootName,
		Path: rootName,
	}

	if err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == absRoot {
			return nil
		}

		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		parentRel := filepath.ToSlash(filepath.Dir(rel))
		parentID := rootID
		if parentRel != "." {
			parentID = localFolderID(parentRel)
		}
		displayPath := rootName + "/" + relSlash

		if d.IsDir() {
			scan.Folders[localFolderID(relSlash)] = &localFolderNode{
				ID:       localFolderID(relSlash),
				Name:     d.Name(),
				ParentID: parentID,
				Path:     displayPath,
			}
			return nil
		}
		if d.Type()&fs.ModeType != 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		file := &drive.LocalFileNode{
			ID:        localFileID(path, info),
			Name:      d.Name(),
			Path:      displayPath,
			LocalPath: path,
			Size:      info.Size(),
			ParentID:  parentID,
		}
		scan.Files = append(scan.Files, file)
		scan.TotalBytes += info.Size()
		return nil
	}); err != nil {
		return nil, err
	}

	scan.computeFolderCounts()
	return scan, nil
}

func (s *localScanResult) computeFolderCounts() {
	for _, f := range s.Files {
		id := f.ParentID
		for id != "" {
			folder := s.Folders[id]
			if folder == nil {
				break
			}
			folder.TotalFiles++
			id = folder.ParentID
		}
	}
}

func (s *localScanResult) emptyFoldersCount() int {
	n := 0
	for _, f := range s.Folders {
		if f.TotalFiles == 0 {
			n++
		}
	}
	return n
}

func createLocalTargetFolders(
	ctx context.Context,
	client *drive.Client,
	scan *localScanResult,
	targetRootID string,
	skipEmpty bool,
) (map[string]string, error) {
	mapping := map[string]string{scan.RootID: targetRootID}
	folders := make([]*localFolderNode, 0, len(scan.Folders))
	for id, f := range scan.Folders {
		if id == scan.RootID {
			continue
		}
		if skipEmpty && f.TotalFiles == 0 {
			continue
		}
		folders = append(folders, f)
	}
	sort.Slice(folders, func(i, j int) bool {
		return strings.Count(folders[i].ID, "/") < strings.Count(folders[j].ID, "/")
	})

	planBar := (*progress.PlanBar)(nil)
	if len(folders) > 0 {
		planBar = progress.NewPlanBar(len(folders), "Folders")
	}
	for i, f := range folders {
		parentTargetID, ok := mapping[f.ParentID]
		if !ok {
			continue
		}
		newID, err := client.CreateFolder(ctx, parentTargetID, f.Name)
		if err != nil {
			if planBar != nil {
				planBar.Finish()
			}
			return nil, fmt.Errorf("create folder %q: %w", f.Path, err)
		}
		mapping[f.ID] = newID
		if planBar != nil {
			planBar.Set(i + 1)
		}
	}
	if planBar != nil {
		planBar.Finish()
	}
	return mapping, nil
}

func localFolderID(rel string) string {
	if rel == "." || rel == "" {
		return "local-folder:."
	}
	return "local-folder:" + filepath.ToSlash(rel)
}

func localFileID(path string, info fs.FileInfo) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return strings.Join([]string{
		"local-file",
		filepath.ToSlash(filepath.Clean(abs)),
		strconv.FormatInt(info.Size(), 10),
		strconv.FormatInt(info.ModTime().UnixNano(), 10),
	}, ":")
}
