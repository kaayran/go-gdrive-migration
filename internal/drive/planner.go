package drive

import (
	"context"
	"fmt"
	"sync"
)

// Planner creates a mirrored folder structure in target Drive.
// Input: ScanResult of source tree. Output: map sourceFolderID -> targetFolderID.
type Planner struct {
	client *Client
}

func NewPlanner(c *Client) *Planner {
	return &Planner{client: c}
}

// PlanProgressFn is called after each created folder.
type PlanProgressFn func(created, total int)

// CreateFolders traverses scan tree and creates folders in target.
// Empty subtrees (TotalFiles == 0) are skipped when skipEmpty == true.
// targetRootID is a target folder corresponding to source rootID.
func (p *Planner) CreateFolders(
	ctx context.Context,
	scan *ScanResult,
	targetRootID string,
	skipEmpty bool,
	progress PlanProgressFn,
) (map[string]string, int, error) {

	mapping := make(map[string]string, len(scan.Folders))
	mapping[scan.RootID] = targetRootID
	var mu sync.Mutex

	// children: parentID → []folderID
	children := make(map[string][]string)
	for id, f := range scan.Folders {
		if f.ParentID != "" {
			children[f.ParentID] = append(children[f.ParentID], id)
		}
	}

	// Level-order BFS so parents are always created before children.
	queue := []string{scan.RootID}
	created := 0

	// Count how many folders actually need to be created (for progress).
	total := 0
	var countSubtree func(id string) bool
	countSubtree = func(id string) bool {
		f := scan.Folders[id]
		hasContent := f.FileCount > 0
		for _, ch := range children[id] {
			if countSubtree(ch) {
				hasContent = true
			}
		}
		if id != scan.RootID && (!skipEmpty || hasContent) {
			total++
		}
		return hasContent
	}
	countSubtree(scan.RootID)

	for len(queue) > 0 {
		next := make([]string, 0, len(queue)*2)
		for _, parentID := range queue {
			parentTargetID, ok := mapping[parentID]
			if !ok {
				// Parent was skipped as empty -> skip descendants too.
				continue
			}
			for _, childID := range children[parentID] {
				child := scan.Folders[childID]
				if skipEmpty && child.TotalFiles == 0 {
					continue
				}
				newID, err := p.client.CreateFolder(ctx, parentTargetID, child.Name)
				if err != nil {
					return nil, created, fmt.Errorf("create folder %q: %w", child.Path, err)
				}
				mu.Lock()
				mapping[childID] = newID
				mu.Unlock()
				created++
				if progress != nil {
					progress(created, total)
				}
				next = append(next, childID)
			}
		}
		queue = next
	}

	return mapping, created, nil
}
