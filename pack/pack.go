// Package pack builds agent packages.
//
// A package (.kagent) is a tar archive holding, in this order:
//
//	manifest.toml   the agent's manifest
//	image.tar       its container image, as an OCI archive
//
// The image holds one file, /agent (a static executable), and runs as
// nobody. Packages are reproducible: the same manifest and executable
// always give the same bytes, so anyone can rebuild an agent from its
// sources and check it is the one distributed.
package pack

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/KaelisOff/agent-sdk-go/manifest"
)

var epoch = time.Unix(0, 0).UTC()

// Result describes a built package.
type Result struct {
	ID, Version string
	// Image is the name the box gives the image.
	Image string
	// Digest identifies the image (its OCI manifest).
	Digest string
}

// Build writes the package of an agent to out. The manifest is checked
// with the box's rules (package manifest) and stored as given.
func Build(manifestTOML, executable []byte, arch string, out io.Writer) (Result, error) {
	m, err := manifest.Parse(manifestTOML)
	if err != nil {
		return Result{}, err
	}
	if len(executable) == 0 {
		return Result{}, errors.New("empty executable")
	}
	res := Result{ID: m.ID, Version: m.Version, Image: m.Image()}

	// The layer: /agent, plus mount points.
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	for _, d := range []string{"etc/", "tmp/"} {
		mode := int64(0o755)
		if d == "tmp/" {
			mode = 0o1777
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d, Mode: mode, ModTime: epoch, Format: tar.FormatPAX}); err != nil {
			return Result{}, err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "agent", Mode: 0o755, Size: int64(len(executable)), ModTime: epoch, Format: tar.FormatPAX}); err != nil {
		return Result{}, err
	}
	if _, err := tw.Write(executable); err != nil {
		return Result{}, err
	}
	if err := tw.Close(); err != nil {
		return Result{}, err
	}
	// The layer is not compressed: a compressor's output changes from one
	// Go version to the next, and the package's digest must depend only
	// on the sources (the store's catalogue signs it). Downloads are
	// compressed by the transport instead.
	layerBlob := layer.Bytes()
	diffID := digest(layerBlob)

	config, _ := json.Marshal(map[string]any{
		"architecture": arch,
		"os":           "linux",
		"config": map[string]any{
			"Entrypoint": []string{"/agent"},
			"User":       "65534:65534",
			"WorkingDir": "/",
		},
		"rootfs":  map[string]any{"type": "layers", "diff_ids": []string{diffID}},
		"history": []map[string]string{{"created_by": "kagent"}},
	})
	imageManifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        descriptor("application/vnd.oci.image.config.v1+json", config),
		"layers":        []any{descriptor("application/vnd.oci.image.layer.v1.tar", layerBlob)},
	})
	res.Digest = digest(imageManifest)
	desc := descriptor("application/vnd.oci.image.manifest.v1+json", imageManifest)
	desc["annotations"] = map[string]string{"org.opencontainers.image.ref.name": res.Image}
	index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{desc}})

	// The OCI archive.
	var image bytes.Buffer
	iw := tar.NewWriter(&image)
	files := []struct {
		name string
		data []byte
	}{
		{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{"index.json", index},
		{blobPath(config), config},
		{blobPath(layerBlob), layerBlob},
		{blobPath(imageManifest), imageManifest},
	}
	for _, d := range []string{"blobs/", "blobs/sha256/"} {
		if err := iw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: d, Mode: 0o755, ModTime: epoch, Format: tar.FormatPAX}); err != nil {
			return Result{}, err
		}
	}
	for _, f := range files {
		if err := writeFile(iw, f.name, f.data); err != nil {
			return Result{}, err
		}
	}
	if err := iw.Close(); err != nil {
		return Result{}, err
	}

	// The package.
	pw := tar.NewWriter(out)
	if err := writeFile(pw, "manifest.toml", manifestTOML); err != nil {
		return Result{}, err
	}
	if err := writeFile(pw, "image.tar", image.Bytes()); err != nil {
		return Result{}, err
	}
	return res, pw.Close()
}

func writeFile(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: epoch, Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func blobPath(b []byte) string {
	sum := sha256.Sum256(b)
	return "blobs/sha256/" + hex.EncodeToString(sum[:])
}

func descriptor(mediaType string, b []byte) map[string]any {
	return map[string]any{"mediaType": mediaType, "digest": digest(b), "size": len(b)}
}
