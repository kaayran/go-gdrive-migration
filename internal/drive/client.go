package drive

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const folderMime = "application/vnd.google-apps.folder"

// Client wraps *drive.Service with retry settings.
type Client struct {
	Svc *drive.Service

	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

func NewClient(ctx context.Context, httpClient *http.Client, maxAttempts, initialBackoffMs, maxBackoffMs int) (*Client, error) {
	svc, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("create drive service: %w", err)
	}
	return &Client{
		Svc:            svc,
		maxAttempts:    maxAttempts,
		initialBackoff: time.Duration(initialBackoffMs) * time.Millisecond,
		maxBackoff:     time.Duration(maxBackoffMs) * time.Millisecond,
	}, nil
}

// IsFolder checks whether a file is a folder by mimeType.
func IsFolder(f *drive.File) bool {
	return f.MimeType == folderMime
}

// FolderMime returns Drive folder mimeType.
func FolderMime() string { return folderMime }

// ResolveSubFolder resolves a path like "A/B/C" under rootID into folderID.
// If subFolderID is not empty, it is returned as-is.
func (c *Client) ResolveSubFolder(ctx context.Context, rootID, subFolderID, subPath string) (string, error) {
	if subFolderID != "" {
		return subFolderID, nil
	}
	if subPath == "" {
		return rootID, nil
	}
	parts := splitPath(subPath)
	currentID := rootID
	for _, part := range parts {
		nextID, err := c.findChildFolderByName(ctx, currentID, part)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", part, err)
		}
		if nextID == "" {
			return "", fmt.Errorf("subfolder not found: %q under %s", part, currentID)
		}
		currentID = nextID
	}
	return currentID, nil
}

func splitPath(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (c *Client) findChildFolderByName(ctx context.Context, parentID, name string) (string, error) {
	// Escape quotes in name.
	esc := strings.ReplaceAll(name, "'", `\'`)
	q := fmt.Sprintf("'%s' in parents and mimeType = '%s' and name = '%s' and trashed = false",
		parentID, folderMime, esc)

	var result string
	err := c.do(ctx, func() error {
		resp, err := c.Svc.Files.List().
			Q(q).
			Fields("files(id,name)").
			SupportsAllDrives(true).
			IncludeItemsFromAllDrives(true).
			PageSize(10).
			Context(ctx).
			Do()
		if err != nil {
			return err
		}
		if len(resp.Files) > 0 {
			result = resp.Files[0].Id
		}
		return nil
	})
	return result, err
}

// GetFile returns metadata for a single file/folder.
func (c *Client) GetFile(ctx context.Context, id string) (*drive.File, error) {
	var out *drive.File
	err := c.do(ctx, func() error {
		f, err := c.Svc.Files.Get(id).
			Fields("id,name,mimeType,size,parents,md5Checksum").
			SupportsAllDrives(true).
			Context(ctx).
			Do()
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	return out, err
}

// ListChildren returns all direct folder children (with pagination).
// includeMD5=true includes md5Checksum and increases response payload.
func (c *Client) ListChildren(ctx context.Context, parentID string, pageSize int64, includeMD5 bool) ([]*drive.File, error) {
	q := fmt.Sprintf("'%s' in parents and trashed = false", parentID)
	var all []*drive.File
	pageToken := ""
	fields := "nextPageToken, files(id,name,mimeType,size)"
	if includeMD5 {
		fields = "nextPageToken, files(id,name,mimeType,size,md5Checksum)"
	}
	for {
		var resp *drive.FileList
		err := c.do(ctx, func() error {
			call := c.Svc.Files.List().
				Q(q).
				Fields(googleapi.Field(fields)).
				PageSize(pageSize).
				SupportsAllDrives(true).
				IncludeItemsFromAllDrives(true).
				Context(ctx)
			if pageToken != "" {
				call = call.PageToken(pageToken)
			}
			r, err := call.Do()
			if err != nil {
				return err
			}
			resp = r
			return nil
		})
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Files...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return all, nil
}

// CreateFolder creates a folder with the given name under parent.
func (c *Client) CreateFolder(ctx context.Context, parentID, name string) (string, error) {
	var id string
	err := c.do(ctx, func() error {
		f, err := c.Svc.Files.Create(&drive.File{
			Name:     name,
			MimeType: folderMime,
			Parents:  []string{parentID},
		}).
			Fields("id").
			SupportsAllDrives(true).
			Context(ctx).
			Do()
		if err != nil {
			return err
		}
		id = f.Id
		return nil
	})
	return id, err
}

// CopyFile performs server-side file copy into the given parent.
func (c *Client) CopyFile(ctx context.Context, fileID, targetParentID, name string) (*drive.File, error) {
	var out *drive.File
	err := c.do(ctx, func() error {
		f, err := c.Svc.Files.Copy(fileID, &drive.File{
			Name:    name,
			Parents: []string{targetParentID},
		}).
			Fields("id,size,md5Checksum").
			SupportsAllDrives(true).
			Context(ctx).
			Do()
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	return out, err
}

// UploadFile uploads a local file into the given Drive parent folder.
func (c *Client) UploadFile(ctx context.Context, localPath, targetParentID, name string) (*drive.File, error) {
	var out *drive.File
	err := c.do(ctx, func() error {
		fh, err := os.Open(localPath)
		if err != nil {
			return err
		}
		defer fh.Close()

		f, err := c.Svc.Files.Create(&drive.File{
			Name:    name,
			Parents: []string{targetParentID},
		}).
			Media(fh, googleapi.ChunkSize(8*1024*1024)).
			Fields("id,size,md5Checksum").
			SupportsAllDrives(true).
			Context(ctx).
			Do()
		if err != nil {
			return err
		}
		out = f
		return nil
	})
	return out, err
}

// SetFolderColor updates Drive folder color in #RRGGBB format.
func (c *Client) SetFolderColor(ctx context.Context, folderID, colorHex string) error {
	return c.do(ctx, func() error {
		_, err := c.Svc.Files.Update(folderID, &drive.File{
			FolderColorRgb: colorHex,
		}).
			Fields("id,folderColorRgb").
			SupportsAllDrives(true).
			Context(ctx).
			Do()
		return err
	})
}

// do executes a function with retry logic and exponential backoff.
func (c *Client) do(ctx context.Context, fn func() error) error {
	backoff := c.initialBackoff
	var lastErr error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err
		if !isRetryable(err) {
			return err
		}
		if attempt == c.maxAttempts {
			break
		}
		// Jitter +/-25% to spread load.
		jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
		sleep := backoff - backoff/4 + jitter
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
		backoff *= 2
		if backoff > c.maxBackoff {
			backoff = c.maxBackoff
		}
	}
	return fmt.Errorf("after %d attempts: %w", c.maxAttempts, lastErr)
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	var gErr *googleapi.Error
	if errors.As(err, &gErr) {
		switch gErr.Code {
		case http.StatusTooManyRequests, // 429
			http.StatusInternalServerError, // 500
			http.StatusBadGateway,          // 502
			http.StatusServiceUnavailable,  // 503
			http.StatusGatewayTimeout:      // 504
			return true
		case http.StatusForbidden: // 403
			// 403 can be rate-limit related (userRateLimitExceeded): retry.
			for _, e := range gErr.Errors {
				if e.Reason == "userRateLimitExceeded" || e.Reason == "rateLimitExceeded" {
					return true
				}
			}
			return false
		}
		return false
	}
	// Retry common transient network errors as well.
	msg := err.Error()
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "TLS handshake") {
		return true
	}
	return false
}
