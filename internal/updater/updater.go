package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang/glog"
)

// Options configures a configuration bundle update operation.
type Options struct {
	// Source is the GitHub tarball API base, e.g.,
	//   https://api.github.com/repos/khulnasoft-lab/kube-bench/tarball
	Source string
	// Ref is a git ref (branch, tag, or commit), e.g., "main" or "v0.8.0".
	Ref string
	// TargetCfgDir is the path to the local cfg directory (e.g., "./cfg/").
	TargetCfgDir string
	// BackupEnabled enables a timestamped backup of the current cfg directory.
	BackupEnabled bool
	// Checksum is an optional SHA256 (hex) of the tarball for integrity verification.
	Checksum string
}

// validateFilePath ensures that the file path is safe and prevents directory traversal attacks
func validateFilePath(baseDir, filePath string) (string, error) {
	// Clean the file path to remove any .. or . components
	cleanPath := filepath.Clean(filePath)
	
	// Check for path traversal attempts
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("path traversal detected: %s", filePath)
	}
	
	// Join with base directory and clean again
	fullPath := filepath.Join(baseDir, cleanPath)
	fullPath = filepath.Clean(fullPath)
	
	// Ensure the resulting path is still within the base directory
	relPath, err := filepath.Rel(baseDir, fullPath)
	if err != nil {
		return "", fmt.Errorf("failed to get relative path: %w", err)
	}
	
	if strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("path traversal detected: %s", filePath)
	}
	
	return fullPath, nil
}

// safeFilePath validates and returns a safe file path within the base directory
func safeFilePath(baseDir, filePath string) (string, error) {
	return validateFilePath(baseDir, filePath)
}

func verifySHA256(path, expectedHex string) error {
	// Validate the file path to prevent directory traversal
	if _, err := validateFilePath(filepath.Dir(path), filepath.Base(path)); err != nil {
		return fmt.Errorf("invalid file path: %w", err)
	}
	
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Printf("Error closing file: %v", err)
		}
	}()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	// Normalize expected by removing optional 0x prefix and lowercasing
	expected := strings.TrimPrefix(strings.ToLower(expectedHex), "0x")
	if got != expected {
		return fmt.Errorf("sha256 mismatch: got %s want %s", got, expected)
	}
	return nil
}

// resolveLatestRef inspects the GitHub API to find the latest release tag.
// Source is expected to look like: https://api.github.com/repos/{owner}/{repo}/tarball
func resolveLatestRef(ctx context.Context, source string) (string, error) {
	// Extract owner/repo between "/repos/" and "/tarball"
	const repos = "/repos/"
	idx := strings.Index(source, repos)
	if idx == -1 {
		return "", fmt.Errorf("cannot parse owner/repo from source %q", source)
	}
	rest := source[idx+len(repos):]
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		return "", fmt.Errorf("cannot parse owner/repo from source %q", source)
	}
	owner, repo := parts[0], parts[1]
	latestURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return "", err
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			fmt.Printf("Error closing response body: %v", err)
		}
	}()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("unexpected status %s from %s", resp.Status, latestURL)
	}
	// Minimal parse: look for "tag_name":"vX.Y.Z" without pulling a full JSON dependency.
	// This is a small, robust approach to avoid extra deps.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	const key = "\"tag_name\":"
	b := string(body)
	k := strings.Index(b, key)
	if k == -1 {
		return "", fmt.Errorf("tag_name not found in latest release response")
	}
	// naive extract of the JSON string value following tag_name
	after := b[k+len(key):]
	s := strings.Index(after, "\"")
	if s == -1 {
		return "", fmt.Errorf("malformed tag_name in response")
	}
	after = after[s+1:]
	e := strings.Index(after, "\"")
	if e == -1 {
		return "", fmt.Errorf("malformed tag_name in response")
	}
	tag := after[:e]
	if tag == "" {
		return "", fmt.Errorf("empty latest tag")
	}
	return tag, nil
}

// Update downloads the repository tarball at the given ref, extracts the cfg/
// directory and replaces the local cfg/ directory.
func Update(ctx context.Context, opt Options) error {
	if opt.Source == "" || opt.Ref == "" || opt.TargetCfgDir == "" {
		return fmt.Errorf("invalid updater options: source, ref and target cfg dir are required")
	}

	// Support 'latest' shorthand by resolving the most recent release tag.
	ref := opt.Ref
	if strings.EqualFold(ref, "latest") {
		latest, err := resolveLatestRef(ctx, opt.Source)
		if err != nil {
			return fmt.Errorf("resolve latest release: %w", err)
		}
		ref = latest
	}

	url := strings.TrimRight(opt.Source, "/") + "/" + strings.TrimLeft(ref, "/")

	tmpDir, err := os.MkdirTemp("", "kube-bench-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(tmpDir); err != nil {
			glog.V(2).Info(fmt.Sprintf("Error removing temp directory: %v", err))
		}
	}()

	tarPath := filepath.Join(tmpDir, "src.tar.gz")
	if err := download(ctx, url, tarPath); err != nil {
		return fmt.Errorf("download tarball: %w", err)
	}

	if opt.Checksum != "" {
		if err := verifySHA256(tarPath, opt.Checksum); err != nil {
			return fmt.Errorf("checksum verification failed: %w", err)
		}
	}

	extractDir := filepath.Join(tmpDir, "extract")
	if err := os.MkdirAll(extractDir, 0o750); err != nil {
		return fmt.Errorf("mkdir extract: %w", err)
	}
	if err := untarGz(tarPath, extractDir); err != nil {
		return fmt.Errorf("extract tarball: %w", err)
	}

	// The GitHub tarball extracts to a single top-level directory named
	// like: khulnasoft-lab-kube-bench-<sha>/
	top, err := findTopLevelDir(extractDir)
	if err != nil {
		return fmt.Errorf("locate top-level dir: %w", err)
	}

	sourceCfg := filepath.Join(top, "cfg")
	if fi, err := os.Stat(sourceCfg); err != nil || !fi.IsDir() {
		return fmt.Errorf("cfg not found in tarball at %s", sourceCfg)
	}

	// Backup target if requested.
	if opt.BackupEnabled {
		if _, err := os.Stat(opt.TargetCfgDir); err == nil {
			bak := fmt.Sprintf("%s.bak-%s", strings.TrimRight(opt.TargetCfgDir, string(os.PathSeparator)), time.Now().Format("20060102-150405"))
			if err := os.Rename(opt.TargetCfgDir, bak); err != nil {
				return fmt.Errorf("backup cfg dir: %w", err)
			}
		}
	}

	// Recreate target.
	if err := os.RemoveAll(opt.TargetCfgDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove old cfg: %w", err)
	}
	if err := copyDir(sourceCfg, opt.TargetCfgDir); err != nil {
		return fmt.Errorf("copy cfg: %w", err)
	}
	return nil
}

func download(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// If a token is provided via environment, use it to avoid anonymous rate limits.
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			fmt.Printf("Error closing response body: %v", err)
		}
	}()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("unexpected status %s from %s", resp.Status, url)
	}
	
	// Validate the destination file path to prevent directory traversal
	if _, err := validateFilePath(filepath.Dir(dest), filepath.Base(dest)); err != nil {
		return fmt.Errorf("invalid destination path: %w", err)
	}
	
	// Create file with secure permissions (0600 instead of 0644)
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Printf("Error closing file: %v", err)
		}
	}()
	_, err = io.Copy(f, resp.Body)
	return err
}

func untarGz(src, dest string) error {
	// Validate the source file path to prevent directory traversal
	if _, err := validateFilePath(filepath.Dir(src), filepath.Base(src)); err != nil {
		return fmt.Errorf("invalid source path: %w", err)
	}
	
	f, err := os.Open(src) // #nosec G304
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Printf("Error closing file: %v", err)
		}
	}()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer func() {
		if err := gz.Close(); err != nil {
			glog.V(2).Info(fmt.Sprintf("Error closing gzip reader: %v", err))
		}
	}()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		
		// Validate the target path to prevent directory traversal attacks
		target, err := safeFilePath(dest, hdr.Name)
		if err != nil {
			return fmt.Errorf("unsafe tar entry: %w", err)
		}
		
		switch hdr.Typeflag {
		case tar.TypeDir:
			// Use secure directory permissions (0750 instead of hdr.Mode which might be too permissive)
			// Safely convert int64 to uint32 to prevent integer overflow with bounds checking
			var mode uint32
			if hdr.Mode > 0o7777 {
				mode = 0o7777
			} else {
				mode = uint32(hdr.Mode) // #nosec G115
			}
			fileMode := os.FileMode(mode)
			if fileMode.Perm() > 0o750 {
				fileMode = 0o750 | (fileMode &^ 0o777) // Keep other bits but restrict permissions
			}
			if err := os.MkdirAll(target, fileMode); err != nil {
				return err
			}
		case tar.TypeReg:
			// Use secure directory permissions for parent directories
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			
			// Use secure file permissions
			// Safely convert int64 to uint32 to prevent integer overflow with bounds checking
			var mode uint32
			if hdr.Mode > 0o7777 {
				mode = 0o7777
			} else {
				mode = uint32(hdr.Mode) // #nosec G115
			}
			fileMode := os.FileMode(mode)
			if fileMode.Perm() > 0o600 {
				fileMode = 0o600 | (fileMode &^ 0o777) // Keep other bits but restrict permissions
			}
			
			w, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, fileMode) // #nosec G304
			if err != nil {
				return err
			}
						// Add size limit to prevent decompression bomb attacks (G110)
				const maxSize = 100 * 1024 * 1024 // 100MB limit per file
				if hdr.Size > maxSize {
					if err := w.Close(); err != nil {
						return fmt.Errorf("error closing writer: %w", err)
					}
					return fmt.Errorf("file too large: %s (%d bytes > %d bytes)", hdr.Name, hdr.Size, maxSize)
				}
			
			// Use limited reader to prevent decompression bomb attacks
			limitedReader := &io.LimitedReader{R: tr, N: maxSize}
			if _, err := io.Copy(w, limitedReader); err != nil {
				if err := w.Close(); err != nil {
					return fmt.Errorf("error closing writer: %w", err)
				}
				return err
			}
			if err := w.Close(); err != nil {
				fmt.Printf("Error closing writer: %v", err)
			}
		}
	}
	return nil
}

func findTopLevelDir(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(root, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no directory found in %s", root)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		
		// Validate the target path to prevent directory traversal
		target, err := safeFilePath(dst, rel)
		if err != nil {
			return fmt.Errorf("unsafe target path: %w", err)
		}
		
		if d.IsDir() {
			// Use secure directory permissions (0750 instead of 0755)
			return os.MkdirAll(target, 0o750)
		}
		// file
		srcF, err := os.Open(path) // #nosec G304
		if err != nil {
			return err
		}
		defer func() {
			if err := srcF.Close(); err != nil {
				fmt.Printf("Error closing source file: %v", err)
			}
		}()
		
		// Use secure directory permissions for parent directories
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		
		// Use secure file permissions (0600 instead of 0644)
		dstF, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600) // #nosec G304
		if err != nil {
			return err
		}
		if _, err := io.Copy(dstF, srcF); err != nil {
			if err := dstF.Close(); err != nil {
				fmt.Printf("Error closing destination file: %v", err)
			}
			return err
		}
		return dstF.Close()
	})
}
