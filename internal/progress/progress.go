package progress

import (
	"fmt"
	"os"
	"sync"

	"github.com/schollz/progressbar/v3"
)

// CopyBars holds two progress bars: files and bytes.
type CopyBars struct {
	mu    sync.Mutex
	files *progressbar.ProgressBar
	bytes *progressbar.ProgressBar
}

func NewCopyBars(totalFiles int64, totalBytes int64) *CopyBars {
	return &CopyBars{
		files: progressbar.NewOptions64(totalFiles,
			progressbar.OptionSetDescription("Files"),
			progressbar.OptionSetWidth(30),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionThrottle(100_000_000), // 100ms
		),
		bytes: progressbar.NewOptions64(totalBytes,
			progressbar.OptionSetDescription("Bytes"),
			progressbar.OptionSetWidth(30),
			progressbar.OptionShowBytes(true),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionThrottle(100_000_000),
		),
	}
}

// Update expects absolute values (not deltas).
func (b *CopyBars) Update(filesDone, bytesDone int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.files.Set64(filesDone)
	_ = b.bytes.Set64(bytesDone)
}

func (b *CopyBars) Finish() {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.files.Finish()
	_ = b.bytes.Finish()
	fmt.Fprintln(os.Stderr)
}

// SimpleSpinner is for scan/plan stages with unknown total amount.
type SimpleSpinner struct {
	bar *progressbar.ProgressBar
}

func NewSimpleSpinner(desc string) *SimpleSpinner {
	return &SimpleSpinner{
		bar: progressbar.NewOptions(-1,
			progressbar.OptionSetDescription(desc),
			progressbar.OptionSpinnerType(14),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionThrottle(100_000_000),
		),
	}
}

func (s *SimpleSpinner) Tick(msg string) {
	s.bar.Describe(msg)
	_ = s.bar.Add(1)
}

func (s *SimpleSpinner) Finish() {
	_ = s.bar.Finish()
	fmt.Fprintln(os.Stderr)
}

// PlanBar is a regular progress bar for known item counts.
type PlanBar struct {
	bar *progressbar.ProgressBar
}

func NewPlanBar(total int, desc string) *PlanBar {
	return &PlanBar{
		bar: progressbar.NewOptions(total,
			progressbar.OptionSetDescription(desc),
			progressbar.OptionSetWidth(30),
			progressbar.OptionShowCount(),
			progressbar.OptionSetWriter(os.Stderr),
			progressbar.OptionSetRenderBlankState(true),
			progressbar.OptionThrottle(100_000_000),
		),
	}
}

func (p *PlanBar) Set(n int) { _ = p.bar.Set(n) }
func (p *PlanBar) Finish() {
	_ = p.bar.Finish()
	fmt.Fprintln(os.Stderr)
}
