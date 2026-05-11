package drive

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// UploadTask describes one local file upload operation.
type UploadTask struct {
	File           *LocalFileNode
	TargetParentID string
}

// LocalFileNode describes a local file selected for upload.
type LocalFileNode struct {
	ID       string
	Name     string
	Path     string
	LocalPath string
	Size     int64
	ParentID string
}

// UploadResult is the outcome of an upload attempt.
type UploadResult struct {
	Task    UploadTask
	NewID   string
	NewMD5  string
	Skipped bool
	Error   error
}

// Uploader runs local file uploads in N parallel workers.
type Uploader struct {
	client     *Client
	workers    int
	onProgress CopyProgressFn
	onResult   func(UploadResult)
}

func NewUploader(c *Client, workers int) *Uploader {
	if workers <= 0 {
		workers = 4
	}
	return &Uploader{client: c, workers: workers}
}

func (u *Uploader) OnProgress(fn CopyProgressFn) { u.onProgress = fn }
func (u *Uploader) OnResult(fn func(UploadResult)) { u.onResult = fn }

func (u *Uploader) Run(
	ctx context.Context,
	tasks []UploadTask,
	totalBytes int64,
	shouldSkip func(fileID string) bool,
) error {
	taskCh := make(chan UploadTask, u.workers*2)
	var wg sync.WaitGroup
	var (
		filesDone int64
		bytesDone int64
		errors    int64
	)
	totalFiles := int64(len(tasks))

	notify := func() {
		if u.onProgress == nil {
			return
		}
		u.onProgress(CopyProgress{
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
			res := UploadResult{Task: t}

			if shouldSkip != nil && shouldSkip(t.File.ID) {
				res.Skipped = true
				atomic.AddInt64(&filesDone, 1)
				atomic.AddInt64(&bytesDone, t.File.Size)
				if u.onResult != nil {
					u.onResult(res)
				}
				notify()
				continue
			}

			f, err := u.client.UploadFile(ctx, t.File.LocalPath, t.TargetParentID, t.File.Name)
			if err != nil {
				res.Error = fmt.Errorf("upload %s: %w", t.File.Path, err)
				atomic.AddInt64(&errors, 1)
			} else {
				res.NewID = f.Id
				res.NewMD5 = f.Md5Checksum
				atomic.AddInt64(&filesDone, 1)
				atomic.AddInt64(&bytesDone, t.File.Size)
			}
			if u.onResult != nil {
				u.onResult(res)
			}
			notify()
		}
	}

	for i := 0; i < u.workers; i++ {
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
		return fmt.Errorf("upload completed with %d errors", e)
	}
	return nil
}
