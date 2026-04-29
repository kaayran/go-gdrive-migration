package drive

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// CopyTask describes one file copy operation.
type CopyTask struct {
	File           *FileNode
	TargetParentID string
}

// CopyResult is the outcome of a copy attempt.
type CopyResult struct {
	Task    CopyTask
	NewID   string
	NewMD5  string
	Skipped bool   // true when file was already copied (per manifest)
	Error   error
}

// CopyProgress is emitted to callback after each file.
type CopyProgress struct {
	FilesDone   int64
	BytesDone   int64
	FilesTotal  int64
	BytesTotal  int64
	Errors      int64
}

type CopyProgressFn func(CopyProgress)

// Copier runs server-side copy for all files in N parallel workers.
type Copier struct {
	client     *Client
	workers    int
	onProgress CopyProgressFn
	onResult   func(CopyResult)
}

func NewCopier(c *Client, workers int) *Copier {
	if workers <= 0 {
		workers = 12
	}
	return &Copier{client: c, workers: workers}
}

func (c *Copier) OnProgress(fn CopyProgressFn) { c.onProgress = fn }
func (c *Copier) OnResult(fn func(CopyResult)) { c.onResult = fn }

// Run copies all files from tasks. shouldSkip can skip already copied files
// (determined by manifest at pipeline level).
func (c *Copier) Run(
	ctx context.Context,
	tasks []CopyTask,
	totalBytes int64,
	shouldSkip func(fileID string) bool,
) error {
	taskCh := make(chan CopyTask, c.workers*2)
	var wg sync.WaitGroup
	var (
		filesDone int64
		bytesDone int64
		errors    int64
	)
	totalFiles := int64(len(tasks))

	notify := func() {
		if c.onProgress == nil {
			return
		}
		c.onProgress(CopyProgress{
			FilesDone:  atomic.LoadInt64(&filesDone),
			BytesDone:  atomic.LoadInt64(&bytesDone),
			FilesTotal: totalFiles,
			BytesTotal: totalBytes,
			Errors:     atomic.LoadInt64(&errors),
		})
	}

	worker := func() {
		defer wg.Done()
		for t := range taskCh {
			if ctx.Err() != nil {
				return
			}
			res := CopyResult{Task: t}

			if shouldSkip != nil && shouldSkip(t.File.ID) {
				res.Skipped = true
				atomic.AddInt64(&filesDone, 1)
				atomic.AddInt64(&bytesDone, t.File.Size)
				if c.onResult != nil {
					c.onResult(res)
				}
				notify()
				continue
			}

			f, err := c.client.CopyFile(ctx, t.File.ID, t.TargetParentID, t.File.Name)
			if err != nil {
				res.Error = fmt.Errorf("copy %s: %w", t.File.Path, err)
				atomic.AddInt64(&errors, 1)
			} else {
				res.NewID = f.Id
				res.NewMD5 = f.Md5Checksum
				atomic.AddInt64(&filesDone, 1)
				atomic.AddInt64(&bytesDone, t.File.Size)
			}
			if c.onResult != nil {
				c.onResult(res)
			}
			notify()
		}
	}

	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go worker()
	}

	for _, t := range tasks {
		select {
		case <-ctx.Done():
			close(taskCh)
			wg.Wait()
			return ctx.Err()
		case taskCh <- t:
		}
	}
	close(taskCh)
	wg.Wait()

	notify()

	if e := atomic.LoadInt64(&errors); e > 0 {
		return fmt.Errorf("copy completed with %d errors", e)
	}
	return nil
}
