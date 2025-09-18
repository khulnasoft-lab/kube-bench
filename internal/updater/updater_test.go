package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to create a gzipped tar archive with a top-level dir and a cfg/ subtree.
func makeTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	tr := tar.NewWriter(gz)
	// Ensure a synthetic top-level dir name
	top := "khulnasoft-lab-kube-bench-sha"
	for name, content := range files {
		full := filepath.Join(top, name)
		dir := filepath.Dir(full)
		// write dir headers down the path
		parts := strings.Split(dir, string(os.PathSeparator))
		acc := ""
		for _, p := range parts {
			if p == "." || p == "" {
				continue
			}
			acc = filepath.Join(acc, p)
			hdr := &tar.Header{Name: acc + "/", Mode: 0o755, Typeflag: tar.TypeDir}
			_ = tr.WriteHeader(hdr)
		}
		// write file
		hdr := &tar.Header{Name: full, Mode: 0o644, Size: int64(len(content))}
		if err := tr.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := io.Copy(tr, strings.NewReader(content)); err != nil {
			t.Fatalf("write content: %v", err)
		}
	}
	_ = tr.Close()
	if err := gz.Close(); err != nil {
		t.Errorf("Error closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

func TestVerifySHA256(t *testing.T) {
	f, err := os.CreateTemp("", "sha-*.bin")
	if err != nil { t.Fatal(err) }
	defer os.Remove(f.Name())
	content := []byte("hello world")
	if _, err := f.Write(content); err != nil { t.Fatal(err) }
	if err := f.Close(); err != nil {
		t.Errorf("Error closing file: %v", err)
	}
	h := sha256.Sum256(content)
	expected := hex.EncodeToString(h[:])
	if err := verifySHA256(f.Name(), expected); err != nil {
		t.Fatalf("expected checksum OK, got %v", err)
	}
	if err := verifySHA256(f.Name(), strings.Repeat("0", 64)); err == nil {
		t.Fatalf("expected checksum mismatch error")
	}
}

func TestUpdateCopiesCfgFromTarball(t *testing.T) {
	// Prepare a tarball that contains cfg/cis-1.11/sample.yaml
	files := map[string]string{
		"cfg/cis-1.11/sample.yaml": "key: value\n",
	}
	tarball := makeTarball(t, files)

	// Serve it via httptest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(tarball)
	}))
	defer srv.Close()

	// Target dir
	tmpDir := t.TempDir()
	targetCfg := filepath.Join(tmpDir, "cfg")
	if err := os.MkdirAll(targetCfg, 0o755); err != nil { t.Fatal(err) }
	// Put a marker file to ensure it's replaced
	if err := os.WriteFile(filepath.Join(targetCfg, "old.txt"), []byte("old"), 0o644); err != nil { t.Fatal(err) }

	// Run update (ref can be any non-empty string; server ignores path)
	opts := Options{
		Source:        srv.URL + "/repos/owner/repo/tarball",
		Ref:           "v0.0.0",
		TargetCfgDir:  targetCfg,
		BackupEnabled: true,
	}
	if err := Update(context.Background(), opts); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	// Verify file exists
	data, err := os.ReadFile(filepath.Join(targetCfg, "cis-1.11", "sample.yaml"))
	if err != nil { t.Fatalf("reading extracted file: %v", err) }
	if string(data) != "key: value\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}
