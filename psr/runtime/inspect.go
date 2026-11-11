// Package main validates the producer's single-image OCI layout before upload.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}
type imageManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
}
type inspection struct {
	ImageDigest  string       `json:"image_digest"`
	ConfigDigest string       `json:"config_digest"`
	Blobs        []descriptor `json:"blobs"`
}

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) > 16<<20 {
		return fmt.Errorf("oversize JSON")
	}
	return json.Unmarshal(b, out)
}
func inspect(root, source string) (*inspection, error) {
	var layout struct {
		Version string `json:"imageLayoutVersion"`
	}
	if err := readJSON(filepath.Join(root, "oci-layout"), &layout); err != nil {
		return nil, err
	}
	if layout.Version != "1.0.0" {
		return nil, fmt.Errorf("unsupported OCI layout")
	}
	var index struct {
		SchemaVersion int          `json:"schemaVersion"`
		Manifests     []descriptor `json:"manifests"`
	}
	if err := readJSON(filepath.Join(root, "index.json"), &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != 2 || len(index.Manifests) != 1 {
		return nil, fmt.Errorf("exactly one image manifest required")
	}
	verified := map[string]descriptor{}
	verify := func(d descriptor) error {
		if !digestPattern.MatchString(d.Digest) || d.Size < 0 {
			return fmt.Errorf("invalid descriptor")
		}
		if prev, ok := verified[d.Digest]; ok {
			if prev.Size != d.Size {
				return fmt.Errorf("descriptor size disagreement")
			}
			return nil
		}
		p := filepath.Join(root, "blobs", "sha256", d.Digest[7:])
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() != d.Size {
			return fmt.Errorf("blob type/size mismatch")
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != d.Digest[7:] {
			return fmt.Errorf("blob hash mismatch")
		}
		verified[d.Digest] = d
		return nil
	}
	m := index.Manifests[0]
	if m.MediaType != "application/vnd.oci.image.manifest.v1+json" && m.MediaType != "application/vnd.docker.distribution.manifest.v2+json" {
		return nil, fmt.Errorf("index contains non-image payload")
	}
	if err := verify(m); err != nil {
		return nil, err
	}
	var manifest imageManifest
	if err := readJSON(filepath.Join(root, "blobs", "sha256", m.Digest[7:]), &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 2 || len(manifest.Layers) == 0 {
		return nil, fmt.Errorf("invalid image manifest")
	}
	if err := verify(manifest.Config); err != nil {
		return nil, err
	}
	for _, d := range manifest.Layers {
		if err := verify(d); err != nil {
			return nil, err
		}
	}
	var config struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Config       struct {
			Labels     map[string]string `json:"Labels"`
			Entrypoint []string          `json:"Entrypoint"`
			WorkingDir string            `json:"WorkingDir"`
		} `json:"config"`
	}
	if err := readJSON(filepath.Join(root, "blobs", "sha256", manifest.Config.Digest[7:]), &config); err != nil {
		return nil, err
	}
	if config.OS != "linux" || config.Architecture != "amd64" {
		return nil, fmt.Errorf("platform must be linux/amd64")
	}
	if config.Config.Labels["org.opencontainers.image.revision"] != source {
		return nil, fmt.Errorf("application source label mismatch")
	}
	if len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != "./entrypoint.sh" || config.Config.WorkingDir != "/ragflow" {
		return nil, fmt.Errorf("unexpected runtime entrypoint")
	}
	// Reject unreferenced files, symlinks and special entries, including attestation blobs.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular layout entry")
		}
		if rel == "oci-layout" || rel == "index.json" {
			return nil
		}
		if len(rel) != len("blobs/sha256/")+64 || rel[:len("blobs/sha256/")] != "blobs/sha256/" {
			return fmt.Errorf("unexpected layout entry %q", rel)
		}
		if _, ok := verified["sha256:"+filepath.Base(rel)]; !ok {
			return fmt.Errorf("unreferenced blob")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := &inspection{ImageDigest: m.Digest, ConfigDigest: manifest.Config.Digest}
	for _, d := range verified {
		out.Blobs = append(out.Blobs, d)
	}
	sort.Slice(out.Blobs, func(i, j int) bool { return out.Blobs[i].Digest < out.Blobs[j].Digest })
	return out, nil
}
func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: inspect OCI_LAYOUT APPLICATION_SHA")
		os.Exit(2)
	}
	v, err := inspect(os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
