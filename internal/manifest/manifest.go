package manifest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Status is the result of processing a single file.
type Status string

const (
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
	// StatusTargetRoot is a service entry with target sub-folder ID,
	// used to avoid recreating it on resume.
	StatusTargetRoot Status = "target_root"
)

// Entry is one record in manifest.jsonl.
type Entry struct {
	Scope    string    `json:"scope,omitempty"`
	SrcID    string    `json:"src_id"`
	DstID    string    `json:"dst_id,omitempty"`
	Path     string    `json:"path"`
	Size     int64     `json:"size,omitempty"`
	SrcMD5   string    `json:"src_md5,omitempty"`
	DstMD5   string    `json:"dst_md5,omitempty"`
	Status   Status    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	Time     time.Time `json:"ts"`
}

// Manifest is an append-only JSONL file for resume support.
type Manifest struct {
	path string
	scope string
	mu   sync.Mutex
	w    *bufio.Writer
	f    *os.File

	done       map[string]bool // src_id values that already have status=done
	targetRoot string          // Target sub-folder ID from the latest StatusTargetRoot entry
}

// Open opens manifest. If file exists and resume=true, it loads completed
// entries to skip them. If resume=false, it rewrites the file.
func Open(path string, resume bool, scope string) (*Manifest, error) {
	m := &Manifest{path: path, scope: scope, done: make(map[string]bool)}

	if resume {
		if err := m.loadCompleted(); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		_ = os.Remove(path)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	m.f = f
	m.w = bufio.NewWriter(f)
	return m, nil
}

// CompletedCount returns the number of already copied files from previous runs.
func (m *Manifest) CompletedCount() int {
	return len(m.done)
}

// TargetRootID returns stored target sub-folder ID from previous runs, if any.
func (m *Manifest) TargetRootID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.targetRoot
}

// SetTargetRoot stores target sub-folder ID in manifest (service entry).
func (m *Manifest) SetTargetRoot(srcID, dstID, path string) error {
	if err := m.Append(Entry{
		Scope:  m.scope,
		SrcID:  srcID,
		DstID:  dstID,
		Path:   path,
		Status: StatusTargetRoot,
	}); err != nil {
		return err
	}
	m.mu.Lock()
	m.targetRoot = dstID
	m.mu.Unlock()
	return nil
}

// IsDone checks whether the file was already copied successfully.
func (m *Manifest) IsDone(srcID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done[srcID]
}

// Append writes one record to manifest. Safe for concurrent calls.
func (m *Manifest) Append(e Entry) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Scope == "" {
		e.Scope = m.scope
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.w.Write(data); err != nil {
		return err
	}
	if err := m.w.WriteByte('\n'); err != nil {
		return err
	}
	if e.Status == StatusDone {
		m.done[e.SrcID] = true
	}
	// Flush immediately to reduce progress loss on abrupt termination.
	return m.w.Flush()
}

// Close closes the underlying file.
func (m *Manifest) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w != nil {
		_ = m.w.Flush()
	}
	if m.f != nil {
		return m.f.Close()
	}
	return nil
}

func (m *Manifest) loadCompleted() error {
	f, err := os.Open(m.path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// JSONL can contain long lines (long errors), so increase scanner buffer.
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			// Silently skip malformed lines instead of failing hard.
			continue
		}
		switch e.Status {
		case StatusDone:
			if e.SrcID != "" && m.isEntryInScope(e) {
				m.done[e.SrcID] = true
			}
		case StatusTargetRoot:
			if e.DstID != "" && m.isEntryInScope(e) {
				m.targetRoot = e.DstID
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan manifest: %w", err)
	}
	return nil
}

func (m *Manifest) isEntryInScope(e Entry) bool {
	if m.scope == "" {
		return true
	}
	if e.Scope != "" {
		return e.Scope == m.scope
	}
	// Backward compatibility for old target_root entries without scope:
	// those can be safely matched by src_id == sub_folder_id.
	if e.Status == StatusTargetRoot && e.SrcID != "" {
		return e.SrcID == m.scope
	}
	// Do not bind old done entries without scope to current subtree,
	// to avoid false skips when sub_folder_id changes.
	return false
}
