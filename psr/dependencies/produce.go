// Package main produces digest-pinned public test-dependency OCI artifacts.
package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type imageSpec struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
	Tag        string `json:"upstream_tag"`
	Digest     string `json:"upstream_manifest_digest"`
}

var approved = []imageSpec{
	{Name: "kvrocks", Repository: "apache/kvrocks", Tag: "2.16.0"},
	{Name: "nats", Repository: "library/nats", Tag: "2.14.2"},
	{Name: "infinity", Repository: "infiniflow/infinity", Tag: "v0.7.5-x64-v3"},
	{Name: "minio", Repository: "pgsty/silo", Tag: "RELEASE.2026-08-06T00-00-00Z"},
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform,omitempty"`
}
type manifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	MediaType     string       `json:"mediaType"`
	Config        descriptor   `json:"config"`
	Layers        []descriptor `json:"layers"`
	Manifests     []descriptor `json:"manifests"`
}
type inputLock struct {
	Schema   int         `json:"schema"`
	Platform string      `json:"platform"`
	Images   []imageSpec `json:"images"`
}

var digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var sourceRE = regexp.MustCompile(`^[a-f0-9]{40}$`)

const accept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

func sum(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func validateLock(lock inputLock) error {
	if lock.Schema != 1 || lock.Platform != "linux/amd64" || len(lock.Images) != len(approved) {
		return fmt.Errorf("invalid dependency lock shape")
	}
	seen := map[string]bool{}
	for _, got := range lock.Images {
		found := false
		for _, want := range approved {
			if got.Name == want.Name && got.Repository == want.Repository && got.Tag == want.Tag {
				found = true
			}
		}
		if !found || seen[got.Name] || !digestRE.MatchString(got.Digest) {
			return fmt.Errorf("dependency is not the approved name/repository/tag/digest")
		}
		seen[got.Name] = true
	}
	return nil
}

type registry struct {
	client *http.Client
	token  map[string]string
}

func newRegistry() *registry {
	return &registry{client: &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 8 || req.URL.Scheme != "https" {
			return fmt.Errorf("invalid registry redirect")
		}
		return nil
	}}, token: map[string]string{}}
}
func safeHTTPError(err error) error {
	var request *url.Error
	for errors.As(err, &request) {
		err = request.Err
	}
	return err
}
func (r *registry) authorize(ctx context.Context, repo string) error {
	if r.token[repo] != "" {
		return nil
	}
	endpoint := "https://auth.docker.io/token?" + url.Values{"service": {"registry.docker.io"}, "scope": {"repository:" + repo + ":pull"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	res, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("DockerHub public token transport failed: %w", safeHTTPError(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("DockerHub token HTTP %d", res.StatusCode)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&payload); err != nil {
		return err
	}
	if payload.Token == "" {
		return fmt.Errorf("missing public pull token")
	}
	r.token[repo] = payload.Token
	return nil
}
func (r *registry) get(ctx context.Context, repo, path string) (*http.Response, error) {
	if err := r.authorize(ctx, repo); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry-1.docker.io/v2/"+repo+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.token[repo])
	req.Header.Set("Accept", accept)
	res, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry transport failed: %w", safeHTTPError(err))
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("registry %s/%s HTTP %d", repo, path, res.StatusCode)
	}
	return res, nil
}
func (r *registry) readManifest(ctx context.Context, s imageSpec, reference string) ([]byte, manifest, error) {
	var m manifest
	res, err := r.get(ctx, s.Repository, "manifests/"+reference)
	if err != nil {
		return nil, m, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, m, err
	}
	if digestRE.MatchString(reference) && sum(b) != reference {
		return nil, m, fmt.Errorf("manifest byte hash differs from approved digest")
	}
	if d := res.Header.Get("Docker-Content-Digest"); d != "" && sum(b) != d {
		return nil, m, fmt.Errorf("registry manifest header/body hash disagreement")
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return nil, m, err
	}
	if m.SchemaVersion != 2 {
		return nil, m, fmt.Errorf("schema2 manifest required")
	}
	if m.MediaType == "" {
		m.MediaType = strings.Split(res.Header.Get("Content-Type"), ";")[0]
	}
	return b, m, nil
}
func selectAMD64(m manifest) (descriptor, error) {
	var selected []descriptor
	for _, d := range m.Manifests {
		if d.Platform != nil && d.Platform.OS == "linux" && d.Platform.Architecture == "amd64" && d.Platform.Variant == "" {
			selected = append(selected, d)
		}
	}
	if len(selected) != 1 {
		return descriptor{}, fmt.Errorf("expected exactly one linux/amd64 manifest")
	}
	return selected[0], nil
}
func (r *registry) resolve(ctx context.Context, s imageSpec) (imageSpec, []byte, manifest, error) {
	b, m, err := r.readManifest(ctx, s, s.Tag)
	if err != nil {
		return s, nil, m, err
	}
	if len(m.Manifests) > 0 {
		selected, e := selectAMD64(m)
		if e != nil {
			return s, nil, m, e
		}
		b, m, err = r.readManifest(ctx, s, selected.Digest)
		if err != nil {
			return s, nil, m, err
		}
		if int64(len(b)) != selected.Size {
			return s, nil, m, fmt.Errorf("platform manifest descriptor size disagreement")
		}
	}
	s.Digest = sum(b)
	return s, b, m, nil
}
func validateManifest(m manifest) error {
	if m.SchemaVersion != 2 || len(m.Manifests) != 0 || len(m.Layers) == 0 {
		return fmt.Errorf("single nonempty image manifest required")
	}
	if m.MediaType != "application/vnd.docker.distribution.manifest.v2+json" && m.MediaType != "application/vnd.oci.image.manifest.v1+json" {
		return fmt.Errorf("unsupported image manifest type")
	}
	for _, d := range append([]descriptor{m.Config}, m.Layers...) {
		if !digestRE.MatchString(d.Digest) || d.Size < 1 || d.Size > 4<<30 {
			return fmt.Errorf("invalid blob descriptor")
		}
	}
	return nil
}
func verifyFile(path string, d descriptor) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, d.Size+1))
	if err != nil {
		return err
	}
	if n != d.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != d.Digest {
		return fmt.Errorf("blob byte hash or size mismatch")
	}
	return nil
}
func (r *registry) blob(ctx context.Context, s imageSpec, d descriptor, root string) error {
	dest := filepath.Join(root, d.Digest[7:])
	if _, err := os.Stat(dest); err == nil {
		return verifyFile(dest, d)
	}
	res, err := r.get(ctx, s.Repository, "blobs/"+d.Digest)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	f, err := os.OpenFile(dest+".part", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, io.LimitReader(res.Body, d.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = verifyFile(dest+".part", d); err != nil {
		return err
	}
	return os.Rename(dest+".part", dest)
}
func configPlatform(path string, m manifest) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	if err = json.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if cfg.OS != "linux" || cfg.Architecture != "amd64" || cfg.RootFS.Type != "layers" || len(cfg.RootFS.DiffIDs) != len(m.Layers) {
		return fmt.Errorf("config platform/rootfs mismatch")
	}
	for _, digest := range cfg.RootFS.DiffIDs {
		if !digestRE.MatchString(digest) {
			return fmt.Errorf("invalid rootfs diff ID")
		}
	}
	return nil
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func archiveLayout(root, dest string) error {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular OCI entry")
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	for _, path := range files {
		st, err := os.Stat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0644, Size: st.Size(), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, in)
		in.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (r *registry) produce(ctx context.Context, s imageSpec, producer, out string) error {
	b, m, err := r.readManifest(ctx, s, s.Digest)
	if err != nil {
		return err
	}
	if err = validateManifest(m); err != nil {
		return err
	}
	root := filepath.Join(out, s.Name)
	if err = os.Mkdir(root, 0755); err != nil {
		return err
	}
	layout := filepath.Join(root, "layout")
	blobRoot := filepath.Join(layout, "blobs", "sha256")
	if err = os.MkdirAll(blobRoot, 0755); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "upstream-manifest.json"), b, 0644); err != nil {
		return err
	}
	image := descriptor{Digest: s.Digest, Size: int64(len(b)), MediaType: m.MediaType}
	if err = os.WriteFile(filepath.Join(blobRoot, s.Digest[7:]), b, 0644); err != nil {
		return err
	}
	descriptors := append([]descriptor{image, m.Config}, m.Layers...)
	seen := map[string]descriptor{s.Digest: image}
	for _, d := range descriptors[1:] {
		if previous, ok := seen[d.Digest]; ok {
			if previous.Size != d.Size {
				return fmt.Errorf("inconsistent blob size")
			}
			continue
		}
		if err = r.blob(ctx, s, d, blobRoot); err != nil {
			return err
		}
		seen[d.Digest] = d
	}
	if err = configPlatform(filepath.Join(blobRoot, m.Config.Digest[7:]), m); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(layout, "oci-layout"), map[string]string{"imageLayoutVersion": "1.0.0"}); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(layout, "index.json"), map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []descriptor{image}}); err != nil {
		return err
	}
	archive := filepath.Join(root, "image.oci.tar")
	if err = archiveLayout(layout, archive); err != nil {
		return err
	}
	archiveHash, err := fileDigest(archive)
	if err != nil {
		return err
	}
	blobs := make([]descriptor, 0, len(seen))
	for _, d := range seen {
		blobs = append(blobs, d)
	}
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Digest < blobs[j].Digest })
	receipt := map[string]any{"schema": 1, "runtime_kind": "test-dependency", "name": s.Name, "repository": s.Repository, "upstream_tag": s.Tag, "upstream_manifest_digest": s.Digest, "platform": "linux/amd64", "producer_source_sha": producer, "image_digest": s.Digest, "config_digest": m.Config.Digest, "archive_sha256": archiveHash, "blobs": blobs}
	if err = writeJSON(filepath.Join(root, "source-manifest.json"), receipt); err != nil {
		return err
	}
	// The uploaded directory contains exactly the three contract files.
	if err = os.RemoveAll(layout); err != nil {
		return err
	}
	fmt.Printf("produced name=%s upstream=%s config=%s archive_sha256=%s\n", s.Name, s.Digest, m.Config.Digest, archiveHash)
	return nil
}
func run(args []string) error {
	registry := newRegistry()
	ctx := context.Background()
	if len(args) == 1 && args[0] == "audit" {
		lock := inputLock{Schema: 1, Platform: "linux/amd64"}
		for _, s := range approved {
			resolved, b, m, err := registry.resolve(ctx, s)
			if err != nil {
				return err
			}
			if err = validateManifest(m); err != nil {
				return err
			}
			if sum(b) != resolved.Digest {
				return fmt.Errorf("resolved hash disagreement")
			}
			lock.Images = append(lock.Images, resolved)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(lock)
	}
	if len(args) != 2 || !sourceRE.MatchString(args[1]) {
		return fmt.Errorf("usage: dependencies OUTPUT_ROOT PRODUCER_SHA; or dependencies audit")
	}
	b, err := os.ReadFile("psr/dependencies/inputs.lock.json")
	if err != nil {
		return err
	}
	var lock inputLock
	if err = json.Unmarshal(b, &lock); err != nil {
		return err
	}
	if err = validateLock(lock); err != nil {
		return err
	}
	out, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if err = os.Mkdir(out, 0755); err != nil {
		return err
	}
	for _, s := range lock.Images {
		if err = registry.produce(ctx, s, args[1], out); err != nil {
			return fmt.Errorf("%s: %w", s.Name, err)
		}
	}
	return nil
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
