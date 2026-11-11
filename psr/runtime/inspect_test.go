package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const testSource = "69b1bbfe0f7e8b6af77ab1aaa9182fb2366cccdb"

func fixture(t *testing.T, arch, source string) (string, descriptor) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	blob := func(v any) descriptor {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		h := fmt.Sprintf("%x", sha256.Sum256(b))
		write("blobs/sha256/"+h, b)
		return descriptor{Digest: "sha256:" + h, Size: int64(len(b))}
	}
	config := blob(map[string]any{"os": "linux", "architecture": arch, "config": map[string]any{"Labels": map[string]string{"org.opencontainers.image.revision": source}, "Entrypoint": []string{"./entrypoint.sh"}, "WorkingDir": "/ragflow"}})
	layer := blob(map[string]string{"fixture": "layer"})
	manifest := blob(imageManifest{SchemaVersion: 2, Config: config, Layers: []descriptor{layer}})
	manifest.MediaType = "application/vnd.oci.image.manifest.v1+json"
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []descriptor{manifest}})
	write("index.json", index)
	write("oci-layout", []byte("{\"imageLayoutVersion\":\"1.0.0\"}"))
	return root, layer
}
func TestValidCompleteRuntimeIdentity(t *testing.T) {
	root, _ := fixture(t, "amd64", testSource)
	r, err := inspect(root, testSource)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Blobs) != 3 {
		t.Fatalf("blob count %d", len(r.Blobs))
	}
}
func TestRejectWrongPlatform(t *testing.T) {
	root, _ := fixture(t, "arm64", testSource)
	if _, err := inspect(root, testSource); err == nil {
		t.Fatal("accepted ARM payload")
	}
}
func TestRejectWrongApplicationSource(t *testing.T) {
	root, _ := fixture(t, "amd64", "c37f5b6f39cc177e283dd960d7150d787926143c")
	if _, err := inspect(root, testSource); err == nil {
		t.Fatal("accepted baseline")
	}
}
func TestRejectTamperedBlob(t *testing.T) {
	root, d := fixture(t, "amd64", testSource)
	if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", d.Digest[7:]), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect(root, testSource); err == nil {
		t.Fatal("accepted tampered blob")
	}
}
func TestRejectUnreferencedBlob(t *testing.T) {
	root, _ := fixture(t, "amd64", testSource)
	if err := os.WriteFile(filepath.Join(root, "blobs", "sha256", fmt.Sprintf("%064d", 0)), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect(root, testSource); err == nil {
		t.Fatal("accepted hidden blob")
	}
}
func TestRejectSymlink(t *testing.T) {
	root, _ := fixture(t, "amd64", testSource)
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect(root, testSource); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestRejectMultipleManifests(t *testing.T) {
	root, _ := fixture(t, "amd64", testSource)
	b, _ := os.ReadFile(filepath.Join(root, "index.json"))
	var idx map[string]json.RawMessage
	json.Unmarshal(b, &idx)
	var manifests []descriptor
	json.Unmarshal(idx["manifests"], &manifests)
	b, _ = json.Marshal(map[string]any{"schemaVersion": 2, "manifests": append(manifests, manifests...)})
	os.WriteFile(filepath.Join(root, "index.json"), b, 0600)
	if _, err := inspect(root, testSource); err == nil {
		t.Fatal("accepted multiple images")
	}
}
