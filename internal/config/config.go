package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config describes config.yaml structure.
type Config struct {
	Auth Auth `yaml:"auth"`

	SourceFolderID string `yaml:"source_folder_id"`
	TargetFolderID string `yaml:"target_folder_id"`

	// SubFolder can be set as "A/B/C" relative path under source_folder_id,
	// or as a direct ID via SubFolderID. If both are set, SubFolderID wins.
	SubFolder   string `yaml:"sub_folder"`
	SubFolderID string `yaml:"sub_folder_id"`

	Options Options `yaml:"options"`

	// Config directory path (filled on load), used to resolve
	// relative paths inside config.
	configDir string `yaml:"-"`
}

// SubFolderIDs returns sub_folder_id list from config.
// Supported separators: comma, ';', newline.
func (c *Config) SubFolderIDs() []string {
	return splitFolderRefs(c.SubFolderID)
}

// SubFolders returns sub_folder path list from config.
// Supported separators: comma, ';', newline.
func (c *Config) SubFolders() []string {
	return splitFolderRefs(c.SubFolder)
}

type Auth struct {
	CredentialsFile string `yaml:"credentials_file"`
	TokenFile       string `yaml:"token_file"`
}

type Options struct {
	SkipEmptyFolders bool   `yaml:"skip_empty_folders"`
	Resume           bool   `yaml:"resume"`
	ManifestFile     string `yaml:"manifest_file"`
	TargetSubfolderPostfix string `yaml:"target_subfolder_postfix"`
	EstimateOnly     bool   `yaml:"estimate_only"`
	// SkipScan disables source pre-flight scan.
	// false (default) scans first, true copies immediately.
	SkipScan bool `yaml:"skip_scan"`

	ScanWorkers   int `yaml:"scan_workers"`
	CopyWorkers   int `yaml:"copy_workers"`
	ListPageSize  int `yaml:"list_page_size"`

	DryRun           bool `yaml:"dry_run"`
	VerifyChecksums  bool `yaml:"verify_checksums"`
	AssumeYes        bool `yaml:"assume_yes"`

	Retry Retry `yaml:"retry"`
}

// IsScanEnabled returns true when pre-flight scan is enabled.
func (o Options) IsScanEnabled() bool {
	return !o.SkipScan
}

type Retry struct {
	MaxAttempts      int `yaml:"max_attempts"`
	InitialBackoffMs int `yaml:"initial_backoff_ms"`
	MaxBackoffMs     int `yaml:"max_backoff_ms"`
}

// Load reads and validates config.
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	c.configDir = filepath.Dir(abs)
	c.applyDefaults()
	if err := c.normalizeFolderRefs(); err != nil {
		return nil, err
	}
	c.resolvePaths()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Auth.CredentialsFile == "" {
		c.Auth.CredentialsFile = "credentials.json"
	}
	if c.Auth.TokenFile == "" {
		c.Auth.TokenFile = "token.json"
	}
	if c.Options.ManifestFile == "" {
		c.Options.ManifestFile = "manifest.jsonl"
	}
	if c.Options.ScanWorkers <= 0 {
		c.Options.ScanWorkers = 16
	}
	if c.Options.CopyWorkers <= 0 {
		c.Options.CopyWorkers = 12
	}
	if c.Options.ListPageSize <= 0 {
		c.Options.ListPageSize = 1000
	}
	if c.Options.Retry.MaxAttempts <= 0 {
		c.Options.Retry.MaxAttempts = 8
	}
	if c.Options.Retry.InitialBackoffMs <= 0 {
		c.Options.Retry.InitialBackoffMs = 500
	}
	if c.Options.Retry.MaxBackoffMs <= 0 {
		c.Options.Retry.MaxBackoffMs = 60000
	}
}

// resolvePaths makes configured file paths absolute relative to config directory.
// This allows running the binary from any location.
func (c *Config) resolvePaths() {
	c.Auth.CredentialsFile = c.resolve(c.Auth.CredentialsFile)
	c.Auth.TokenFile = c.resolve(c.Auth.TokenFile)
	c.Options.ManifestFile = c.resolve(c.Options.ManifestFile)
}

func (c *Config) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.configDir, p)
}

func (c *Config) validate() error {
	if c.SourceFolderID == "" {
		return fmt.Errorf("source_folder_id is required")
	}
	if c.TargetFolderID == "" {
		return fmt.Errorf("target_folder_id is required")
	}
	if len(c.SubFolders()) == 0 && len(c.SubFolderIDs()) == 0 {
		return fmt.Errorf("either sub_folder or sub_folder_id is required")
	}
	if _, err := os.Stat(c.Auth.CredentialsFile); err != nil {
		return fmt.Errorf("credentials file not found: %s", c.Auth.CredentialsFile)
	}
	return nil
}

func (c *Config) normalizeFolderRefs() error {
	var err error
	c.SourceFolderID, err = normalizeFolderRef(c.SourceFolderID)
	if err != nil {
		return fmt.Errorf("source_folder_id: %w", err)
	}

	c.TargetFolderID, err = normalizeFolderRef(c.TargetFolderID)
	if err != nil {
		return fmt.Errorf("target_folder_id: %w", err)
	}

	subIDs := splitFolderRefs(c.SubFolderID)
	if len(subIDs) > 0 {
		normalized := make([]string, 0, len(subIDs))
		for i, raw := range subIDs {
			id, normErr := normalizeFolderRef(raw)
			if normErr != nil {
				return fmt.Errorf("sub_folder_id[%d]: %w", i, normErr)
			}
			if id != "" {
				normalized = append(normalized, id)
			}
		}
		c.SubFolderID = strings.Join(normalized, ",")
	}
	if subFolders := splitFolderRefs(c.SubFolder); len(subFolders) > 0 {
		c.SubFolder = strings.Join(subFolders, ",")
	}

	return nil
}

func splitFolderRefs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeFolderRef(raw string) (string, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return "", nil
	}

	if !strings.Contains(ref, "://") && strings.Contains(ref, "drive.google.com/") {
		ref = "https://" + ref
	}

	if !strings.Contains(ref, "://") {
		// Looks like a plain ID.
		return ref, nil
	}

	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	if id := strings.TrimSpace(u.Query().Get("id")); id != "" {
		return id, nil
	}

	pathParts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i < len(pathParts)-1; i++ {
		if pathParts[i] == "folders" && pathParts[i+1] != "" {
			return pathParts[i+1], nil
		}
	}

	if strings.Contains(strings.ToLower(u.Host), "drive.google.com") {
		return "", fmt.Errorf("could not extract folder ID from URL %q", raw)
	}

	// Not a Google Drive URL; keep as-is (may already be an unconventional ID format).
	return strings.TrimSpace(raw), nil
}
