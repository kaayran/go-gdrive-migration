package drive

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// FileNode describes a file in the scanned tree.
type FileNode struct {
	ID       string
	Name     string
	Size     int64
	MD5      string
	ParentID string // Parent folder ID in SOURCE
	Path     string // Relative path (for logs and manifest)
}

// FolderNode describes a folder in the scanned tree.
type FolderNode struct {
	ID         string
	Name       string
	ParentID   string // Parent folder ID in SOURCE; "" for scan root
	Path       string // Relative path
	FileCount  int    // Direct files
	TotalFiles int    // Files in subtree (filled in second pass)
}

// ScanResult is the stage 1 output.
type ScanResult struct {
	RootID  string                 // Scan root ID (= subFolderID)
	Files   []*FileNode            // All files in subtree
	Folders map[string]*FolderNode // All folders in subtree, key = ID

	TotalBytes int64

	ShortcutsFollowed  int64 // shortcuts resolved to their targets and included
	ShortcutsSkipped   int64 // shortcuts skipped (broken target or no access)
	ShortcutDuplicates int64 // targets already present in the tree (copied once)
}

// ScanProgress is emitted via callback during scanning.
type ScanProgress struct {
	Folders int64
	Files   int64
	Bytes   int64
}

type ScanProgressFn func(ScanProgress)

// EstimateResult is a lightweight pre-copy estimate result.
type EstimateResult struct {
	Folders           int64
	Files             int64
	Bytes             int64
	ShortcutsFollowed int64 // shortcuts resolved to their targets and included
	ShortcutsSkipped  int64 // shortcuts skipped (broken target or no access)
}

// Scanner performs parallel BFS over the Drive tree.
type Scanner struct {
	client       *Client
	workers      int
	pageSize     int64
	includeMD5   bool
	onProgress   ScanProgressFn
}

func NewScanner(c *Client, workers, pageSize int, includeMD5 bool) *Scanner {
	if workers <= 0 {
		workers = 16
	}
	if pageSize <= 0 {
		pageSize = 1000
	}
	return &Scanner{client: c, workers: workers, pageSize: int64(pageSize), includeMD5: includeMD5}
}

func (s *Scanner) OnProgress(fn ScanProgressFn) { s.onProgress = fn }

type workQueue[T any] struct {
	mu      sync.Mutex
	items   []T
	pending int
	closed  bool
	notify  chan struct{}
}

func newWorkQueue[T any]() *workQueue[T] {
	return &workQueue[T]{notify: make(chan struct{}, 1)}
}

func (q *workQueue[T]) Enqueue(item T) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.items = append(q.items, item)
	q.pending++
	q.signalLocked()
	return true
}

func (q *workQueue[T]) Dequeue(ctx context.Context) (T, bool) {
	var zero T
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return item, true
		}
		if q.closed {
			q.mu.Unlock()
			return zero, false
		}
		q.mu.Unlock()

		select {
		case <-q.notify:
		case <-ctx.Done():
			return zero, false
		}
	}
}

func (q *workQueue[T]) CompleteTask() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending > 0 {
		q.pending--
	}
	if q.pending == 0 && !q.closed {
		q.closed = true
		q.signalLocked()
	}
}

func (q *workQueue[T]) signalLocked() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Estimate computes quick size/count stats without storing a full tree in memory.
func (s *Scanner) Estimate(ctx context.Context, rootID string) (*EstimateResult, error) {
	var (
		wg                sync.WaitGroup
		errCh             = make(chan error, 1)
		filesCnt          int64
		bytesCnt          int64
		foldersCnt        int64 = 1 // root already counted
		shortcutsFollowed int64
		shortcutsSkipped  int64
	)
	queue := newWorkQueue[string]()

	// visited protects against shortcut cycles and duplicate targets.
	var visitedMu sync.Mutex
	visited := map[string]struct{}{rootID: {}}
	enqueueUnique := func(id string) bool {
		visitedMu.Lock()
		if _, ok := visited[id]; ok {
			visitedMu.Unlock()
			return false
		}
		visited[id] = struct{}{}
		visitedMu.Unlock()
		return queue.Enqueue(id)
	}

	var lastNotifyNs int64
	notify := func(force bool) {
		if s.onProgress == nil {
			return
		}
		if !force {
			now := time.Now().UnixNano()
			prev := atomic.LoadInt64(&lastNotifyNs)
			if prev != 0 && now-prev < int64(250*time.Millisecond) {
				return
			}
			if !atomic.CompareAndSwapInt64(&lastNotifyNs, prev, now) {
				return
			}
		}
		s.onProgress(ScanProgress{
			Folders: atomic.LoadInt64(&foldersCnt),
			Files:   atomic.LoadInt64(&filesCnt),
			Bytes:   atomic.LoadInt64(&bytesCnt),
		})
	}

	worker := func() {
		defer wg.Done()
		for {
			folderID, ok := queue.Dequeue(ctx)
			if !ok {
				return
			}
			func() {
				defer queue.CompleteTask()

			children, err := s.client.ListChildren(ctx, folderID, s.pageSize, false)
			if err != nil {
				select {
				case errCh <- fmt.Errorf("list %s: %w", folderID, err):
				default:
				}
				return
			}

			localFolders := make([]string, 0, len(children))
			localFiles := int64(0)
			localBytes := int64(0)
			for _, ch := range children {
				if IsShortcut(ch) {
					// Follow the shortcut: count the target contents.
					if ch.ShortcutDetails == nil {
						atomic.AddInt64(&shortcutsSkipped, 1)
						continue
					}
					target, terr := s.client.GetFile(ctx, ch.ShortcutDetails.TargetId)
					if terr != nil || IsShortcut(target) {
						atomic.AddInt64(&shortcutsSkipped, 1)
						continue
					}
					if IsFolder(target) {
						localFolders = append(localFolders, target.Id)
					} else {
						localFiles++
						localBytes += target.Size
					}
					atomic.AddInt64(&shortcutsFollowed, 1)
					continue
				}
				if IsFolder(ch) {
					localFolders = append(localFolders, ch.Id)
					continue
				}
				localFiles++
				localBytes += ch.Size
			}

			atomic.AddInt64(&filesCnt, localFiles)
			atomic.AddInt64(&bytesCnt, localBytes)

			newFolders := int64(0)
			for _, subID := range localFolders {
				if enqueueUnique(subID) {
					newFolders++
				}
			}
			atomic.AddInt64(&foldersCnt, newFolders)
			notify(false)
			}()
		}
	}

	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go worker()
	}

	_ = queue.Enqueue(rootID)

	wg.Wait()
	notify(true)

	select {
	case err := <-errCh:
		return nil, err
	default:
	}
	return &EstimateResult{
		Folders:           atomic.LoadInt64(&foldersCnt),
		Files:             atomic.LoadInt64(&filesCnt),
		Bytes:             atomic.LoadInt64(&bytesCnt),
		ShortcutsFollowed: atomic.LoadInt64(&shortcutsFollowed),
		ShortcutsSkipped:  atomic.LoadInt64(&shortcutsSkipped),
	}, nil
}

// Scan traverses the tree from rootID. rootName is used for path prefixes.
func (s *Scanner) Scan(ctx context.Context, rootID, rootName string) (*ScanResult, error) {
	type task struct {
		folder *FolderNode
	}

	res := &ScanResult{
		RootID:  rootID,
		Folders: make(map[string]*FolderNode),
	}

	root := &FolderNode{
		ID:       rootID,
		Name:     rootName,
		ParentID: "",
		Path:     rootName,
	}
	res.Folders[rootID] = root

	var (
		mu                sync.Mutex
		wg                sync.WaitGroup
		errCh             = make(chan error, 1)
		filesCnt          int64
		bytesCnt          int64
		foldersCnt        int64 = 1 // root already counted
		shortcutsFollowed int64
		shortcutsSkipped  int64
		shortcutDupes     int64
	)
	// seenFiles deduplicates files by source ID (a file reachable both
	// directly and via a shortcut is copied once). Guarded by mu.
	seenFiles := make(map[string]struct{})
	queue := newWorkQueue[task]()

	var lastNotifyNs int64
	notify := func(force bool) {
		if s.onProgress == nil {
			return
		}
		if !force {
			now := time.Now().UnixNano()
			prev := atomic.LoadInt64(&lastNotifyNs)
			if prev != 0 && now-prev < int64(250*time.Millisecond) {
				return
			}
			if !atomic.CompareAndSwapInt64(&lastNotifyNs, prev, now) {
				return
			}
		}
		s.onProgress(ScanProgress{
			Folders: atomic.LoadInt64(&foldersCnt),
			Files:   atomic.LoadInt64(&filesCnt),
			Bytes:   atomic.LoadInt64(&bytesCnt),
		})
	}

	worker := func() {
		defer wg.Done()
		for {
			t, ok := queue.Dequeue(ctx)
			if !ok {
				return
			}
			func() {
				defer queue.CompleteTask()

			children, err := s.client.ListChildren(ctx, t.folder.ID, s.pageSize, s.includeMD5)
			if err != nil {
				select {
				case errCh <- fmt.Errorf("list %s: %w", t.folder.Path, err):
				default:
				}
				return
			}

			var localFiles []*FileNode
			var localFolders []*FolderNode
			for _, ch := range children {
				if IsShortcut(ch) {
					// Follow the shortcut: copy the target contents.
					if ch.ShortcutDetails == nil {
						atomic.AddInt64(&shortcutsSkipped, 1)
						continue
					}
					target, terr := s.client.GetFile(ctx, ch.ShortcutDetails.TargetId)
					if terr != nil || IsShortcut(target) {
						atomic.AddInt64(&shortcutsSkipped, 1)
						continue
					}
					if IsFolder(target) {
						sub := &FolderNode{
							ID:       target.Id,
							Name:     ch.Name,
							ParentID: t.folder.ID,
							Path:     t.folder.Path + "/" + ch.Name,
						}
						localFolders = append(localFolders, sub)
					} else {
						f := &FileNode{
							ID:       target.Id,
							Name:     ch.Name,
							Size:     target.Size,
							MD5:      target.Md5Checksum,
							ParentID: t.folder.ID,
							Path:     t.folder.Path + "/" + ch.Name,
						}
						localFiles = append(localFiles, f)
					}
					atomic.AddInt64(&shortcutsFollowed, 1)
					continue
				}
				if IsFolder(ch) {
					sub := &FolderNode{
						ID:       ch.Id,
						Name:     ch.Name,
						ParentID: t.folder.ID,
						Path:     t.folder.Path + "/" + ch.Name,
					}
					localFolders = append(localFolders, sub)
				} else {
					f := &FileNode{
						ID:       ch.Id,
						Name:     ch.Name,
						Size:     ch.Size,
						MD5:      ch.Md5Checksum,
						ParentID: t.folder.ID,
						Path:     t.folder.Path + "/" + ch.Name,
					}
					localFiles = append(localFiles, f)
				}
			}

			// Single lock per folder: collect local data, then merge once.
			// Folders and files are deduplicated by source ID, which also
			// protects against shortcut cycles (each ID is visited once).
			var addFolders []*FolderNode
			var addFiles []*FileNode
			mu.Lock()
			for _, sub := range localFolders {
				if _, exists := res.Folders[sub.ID]; exists {
					atomic.AddInt64(&shortcutDupes, 1)
					continue
				}
				res.Folders[sub.ID] = sub
				addFolders = append(addFolders, sub)
			}
			for _, f := range localFiles {
				if _, exists := seenFiles[f.ID]; exists {
					atomic.AddInt64(&shortcutDupes, 1)
					continue
				}
				seenFiles[f.ID] = struct{}{}
				addFiles = append(addFiles, f)
			}
			res.Files = append(res.Files, addFiles...)
			t.folder.FileCount = len(addFiles)
			mu.Unlock()

			atomic.AddInt64(&filesCnt, int64(len(addFiles)))
			atomic.AddInt64(&foldersCnt, int64(len(addFolders)))
			localBytes := int64(0)
			for _, f := range addFiles {
				localBytes += f.Size
			}
			atomic.AddInt64(&bytesCnt, localBytes)
			notify(false)

			// Enqueue nested folders.
			for _, sub := range addFolders {
				if !queue.Enqueue(task{folder: sub}) {
					return
				}
			}
			}()
		}
	}

	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go worker()
	}

	_ = queue.Enqueue(task{folder: root})

	wg.Wait()
	notify(true)

	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	res.TotalBytes = atomic.LoadInt64(&bytesCnt)
	res.ShortcutsFollowed = atomic.LoadInt64(&shortcutsFollowed)
	res.ShortcutsSkipped = atomic.LoadInt64(&shortcutsSkipped)
	res.ShortcutDuplicates = atomic.LoadInt64(&shortcutDupes)

	// Populate subtree TotalFiles for skip-empty logic.
	computeSubtreeCounts(res)

	return res, nil
}

// computeSubtreeCounts fills FolderNode.TotalFiles bottom-up.
func computeSubtreeCounts(r *ScanResult) {
	// Build children map for each folder first.
	children := make(map[string][]string)
	for id, f := range r.Folders {
		if f.ParentID != "" {
			children[f.ParentID] = append(children[f.ParentID], id)
		}
	}

	var dfs func(id string) int
	dfs = func(id string) int {
		f := r.Folders[id]
		total := f.FileCount
		for _, ch := range children[id] {
			total += dfs(ch)
		}
		f.TotalFiles = total
		return total
	}
	dfs(r.RootID)
}

// HasFilesInSubtree returns true if folder or its subtree has at least one file.
func (r *ScanResult) HasFilesInSubtree(folderID string) bool {
	f, ok := r.Folders[folderID]
	if !ok {
		return false
	}
	return f.TotalFiles > 0
}

// EmptyFoldersCount returns number of folders without files in the subtree.
func (r *ScanResult) EmptyFoldersCount() int {
	n := 0
	for _, f := range r.Folders {
		if f.TotalFiles == 0 {
			n++
		}
	}
	return n
}
