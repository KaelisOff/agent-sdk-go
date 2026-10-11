package pack

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

const testManifest = "id = \"io.github.example.hello\"\nversion = \"1.2.3\"\nname = \"Hello\"\nlicense = \"Apache-2.0\"\nsource = \"https://github.com/example/hello\"\n"

func TestBuildIsReproducible(t *testing.T) {
	var a, b bytes.Buffer
	ra, err := Build([]byte(testManifest), []byte("\x7fELF fake"), "amd64", &a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build([]byte(testManifest), []byte("\x7fELF fake"), "amd64", &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two builds differ")
	}
	if ra.Image != "localhost/kaelis-agent/io.github.example.hello:1.2.3" || !strings.HasPrefix(ra.Digest, "sha256:") {
		t.Errorf("result = %+v", ra)
	}
	var c bytes.Buffer
	rc, _ := Build([]byte(testManifest), []byte("\x7fELF other"), "amd64", &c)
	if rc.Digest == ra.Digest {
		t.Error("different executables, same digest")
	}
}

func TestPackageLayout(t *testing.T) {
	var buf bytes.Buffer
	if _, err := Build([]byte(testManifest), []byte("exe"), "amd64", &buf); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var names []string
	var image []byte
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "image.tar" {
			image, _ = io.ReadAll(tr)
		}
	}
	if strings.Join(names, ",") != "manifest.toml,image.tar" {
		t.Fatalf("package entries = %v", names)
	}
	// The OCI archive names the image for the box.
	ir := tar.NewReader(bytes.NewReader(image))
	for {
		h, err := ir.Next()
		if err != nil {
			t.Fatal("no index.json")
		}
		if h.Name != "index.json" {
			continue
		}
		var idx struct {
			Manifests []struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"manifests"`
		}
		json.NewDecoder(ir).Decode(&idx)
		if idx.Manifests[0].Annotations["org.opencontainers.image.ref.name"] != "localhost/kaelis-agent/io.github.example.hello:1.2.3" {
			t.Errorf("index = %+v", idx)
		}
		return
	}
}

func TestBuildRejects(t *testing.T) {
	for _, m := range []string{
		strings.Replace(testManifest, "io.github.example.hello", "Hello", 1),
		strings.Replace(testManifest, "1.2.3", "latest", 1),
		strings.Replace(testManifest, "license = \"Apache-2.0\"\n", "", 1),
		"not toml [",
	} {
		if _, err := Build([]byte(m), []byte("exe"), "amd64", io.Discard); err == nil {
			t.Errorf("%q accepted", m)
		}
	}
}
