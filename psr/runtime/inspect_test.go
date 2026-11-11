package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func producerFunction(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("produce.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "copy_locked_files() {")
	if start < 0 {
		t.Fatal("missing copy owner")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("missing function end")
	}
	return text[start : start+end+3]
}
func runCopy(t *testing.T, root, manifest, dest string) error {
	t.Helper()
	cmd := exec.Command("bash", "-euc", producerFunction(t)+"\ncopy_locked_files \"$1\" \"$2\" \"$3\"", "copy-test", root, manifest, dest)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("copy refused: %s", b)
	}
	return err
}
func TestCopyOnlyLockedDependencies(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(t.TempDir(), "output")
	os.Mkdir(filepath.Join(root, "nested"), 0700)
	payload := []byte("public resource")
	os.WriteFile(filepath.Join(root, "nested", "known"), payload, 0600)
	os.WriteFile(filepath.Join(root, "unlisted-private-file"), []byte("synthetic extra file"), 0600)
	os.Symlink("/root/ragflow-native-libs/office_oxide/lib/liboffice_oxide.a", filepath.Join(root, "generated-native-version-link"))
	manifest := filepath.Join(t.TempDir(), "checksums")
	os.WriteFile(manifest, []byte(fmt.Sprintf("%x  ./nested/known\n", sha256.Sum256(payload))), 0600)
	if err := runCopy(t, root, manifest, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "nested", "known"))
	if err != nil || string(got) != string(payload) {
		t.Fatal("locked payload not copied")
	}
	if _, err = os.Stat(filepath.Join(dst, "unlisted-private-file")); !os.IsNotExist(err) {
		t.Fatal("unlisted input escaped into artifact")
	}
	if _, err = os.Lstat(filepath.Join(dst, "generated-native-version-link")); !os.IsNotExist(err) {
		t.Fatal("generated absolute native link escaped into archived dependencies")
	}
}
func TestRejectEscapingDependencyPath(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(t.TempDir(), "checksums")
	os.WriteFile(manifest, []byte(fmt.Sprintf("%064d  ../outside\n", 0)), 0600)
	if err := runCopy(t, root, manifest, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("accepted traversal")
	}
}
func TestRejectExternalDependencySymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("synthetic outside file"), 0600)
	os.Symlink(outside, filepath.Join(root, "linked"))
	manifest := filepath.Join(t.TempDir(), "checksums")
	os.WriteFile(manifest, []byte(fmt.Sprintf("%064d  linked\n", 0)), 0600)
	if err := runCopy(t, root, manifest, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("accepted external symlink")
	}
}
