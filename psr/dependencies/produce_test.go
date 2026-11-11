package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validLock() inputLock {
	lock := inputLock{Schema: 1, Platform: "linux/amd64", Images: append([]imageSpec(nil), approved...)}
	for i := range lock.Images {
		lock.Images[i].Digest = "sha256:" + strings.Repeat("a", 64)
	}
	return lock
}
func TestApprovedDependencyLock(t *testing.T) {
	if err := validateLock(validLock()); err != nil {
		t.Fatal(err)
	}
}
func TestRejectUnapprovedDependencyInputs(t *testing.T) {
	cases := map[string]func(*inputLock){
		"repository": func(l *inputLock) { l.Images[0].Repository = "untrusted/kvrocks" },
		"tag":        func(l *inputLock) { l.Images[1].Tag = "latest" },
		"name":       func(l *inputLock) { l.Images[0].Name = "arbitrary" },
		"duplicate":  func(l *inputLock) { l.Images[1] = l.Images[0] },
		"digest":     func(l *inputLock) { l.Images[0].Digest = "sha256:invalid" },
		"platform":   func(l *inputLock) { l.Platform = "linux/arm64" },
		"missing":    func(l *inputLock) { l.Images = l.Images[:3] },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			lock := validLock()
			change(&lock)
			if err := validateLock(lock); err == nil {
				t.Fatal("accepted unapproved dependency input")
			}
		})
	}
}
func TestSelectExactlyOneAMD64Manifest(t *testing.T) {
	var m manifest
	json.Unmarshal([]byte(`{"manifests":[{"digest":"sha256:arm","platform":{"os":"linux","architecture":"arm64"}},{"digest":"sha256:amd","platform":{"os":"linux","architecture":"amd64"}},{"digest":"sha256:attestation","platform":{"os":"unknown","architecture":"unknown"}}]}`), &m)
	d, err := selectAMD64(m)
	if err != nil || d.Digest != "sha256:amd" {
		t.Fatal("wrong platform manifest")
	}
	m.Manifests = append(m.Manifests, m.Manifests[1])
	if _, err = selectAMD64(m); err == nil {
		t.Fatal("accepted ambiguous platform")
	}
}
func TestBlobDigestAndLengthEnforcement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	payload := []byte("registry byte payload")
	os.WriteFile(path, payload, 0600)
	d := descriptor{Digest: sum(payload), Size: int64(len(payload))}
	if err := verifyFile(path, d); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, append(payload, 'x'), 0600)
	if err := verifyFile(path, d); err == nil {
		t.Fatal("accepted appended bytes")
	}
	os.WriteFile(path, bytes.Repeat([]byte("x"), len(payload)), 0600)
	if err := verifyFile(path, d); err == nil {
		t.Fatal("accepted equal-length corruption")
	}
}
func TestDependencyOCIArchivePreservesExactManifestAndBlobs(t *testing.T) {
	root := t.TempDir()
	layout := filepath.Join(root, "layout")
	blobRoot := filepath.Join(layout, "blobs", "sha256")
	os.MkdirAll(blobRoot, 0700)
	original := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json"}`)
	digest := sum(original)
	os.WriteFile(filepath.Join(blobRoot, digest[7:]), original, 0600)
	os.WriteFile(filepath.Join(layout, "oci-layout"), []byte(`{"imageLayoutVersion":"1.0.0"}`), 0600)
	os.WriteFile(filepath.Join(layout, "index.json"), []byte(`{"schemaVersion":2}`), 0600)
	archive := filepath.Join(root, "image.oci.tar")
	if err := archiveLayout(layout, archive); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(archive)
	if err := archiveLayout(layout, archive); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(archive)
	if !bytes.Equal(first, second) {
		t.Fatal("archive is not reproducible")
	}
	tr := tar.NewReader(bytes.NewReader(first))
	seen := false
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(header.Name, "./") || strings.HasPrefix(header.Name, "/") || header.Typeflag != tar.TypeReg {
			t.Fatal("unsafe tar name/type")
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "blobs/sha256/"+digest[7:] {
			seen = true
			if !bytes.Equal(b, original) {
				t.Fatal("upstream manifest changed")
			}
		}
	}
	if !seen {
		t.Fatal("missing upstream manifest blob")
	}
	got, err := fileDigest(archive)
	if err != nil {
		t.Fatal(err)
	}
	if got != fmt.Sprintf("%x", sha256.Sum256(first)) {
		t.Fatal("archive hash mismatch")
	}
}
func TestRejectNonregularOCIFile(t *testing.T) {
	root := t.TempDir()
	os.Symlink("/synthetic/outside", filepath.Join(root, "link"))
	if err := archiveLayout(root, filepath.Join(t.TempDir(), "out.tar")); err == nil {
		t.Fatal("accepted OCI symlink")
	}
}
func TestConfigPlatformAndRootFS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	m := manifest{Layers: []descriptor{{Digest: "sha256:" + strings.Repeat("a", 64)}}}
	good := map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + strings.Repeat("b", 64)}}}
	writeJSON(path, good)
	if err := configPlatform(path, m); err != nil {
		t.Fatal(err)
	}
	good["architecture"] = "arm64"
	writeJSON(path, good)
	if err := configPlatform(path, m); err == nil {
		t.Fatal("accepted config ARM")
	}
	good["architecture"] = "amd64"
	good["rootfs"] = map[string]any{"type": "layers", "diff_ids": []string{}}
	writeJSON(path, good)
	if err := configPlatform(path, m); err == nil {
		t.Fatal("accepted wrong diff ID count")
	}
}

func TestTransportErrorsDoNotExposeSignedURLs(t *testing.T) {
	err := &url.Error{Op: "GET", URL: "https://registry.example/blob?signature=synthetic-secret", Err: errors.New("deadline exceeded")}
	got := safeHTTPError(err).Error()
	if strings.Contains(got, "synthetic-secret") || !strings.Contains(got, "deadline exceeded") {
		t.Fatal("transport error leaked request URL or lost failure")
	}
}
