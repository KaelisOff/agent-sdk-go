// Command kagent checks an agent manifest and builds an agent package
// (.kagent) from it and a static Linux executable.
//
//	kagent -check manifest.toml
//	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags '-s -w' -o agent .
//	kagent -manifest manifest.toml -executable agent -o mon-agent.kagent
//
// The package is reproducible: rebuilding from the same sources gives the
// same bytes and the same image digest.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/KaelisOff/agent-sdk-go/manifest"
	"github.com/KaelisOff/agent-sdk-go/pack"
)

func main() {
	check := flag.String("check", "", "only check this manifest and show what it asks for")
	manifestPath := flag.String("manifest", "manifest.toml", "agent manifest")
	executable := flag.String("executable", "agent", "static Linux executable of the agent")
	arch := flag.String("arch", "amd64", "architecture of the executable")
	out := flag.String("o", "", "output package (default ID-VERSION.kagent)")
	flag.Parse()
	var err error
	if *check != "" {
		err = checkManifest(*check)
	} else {
		err = build(*manifestPath, *executable, *arch, *out)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kagent: %v\n", err)
		os.Exit(1)
	}
}

func checkManifest(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m, err := manifest.Parse(data)
	if err != nil {
		return err
	}
	caps := strings.Join(m.Permissions.Capabilities(), ", ")
	if caps == "" {
		caps = "none"
	}
	fmt.Printf("%s %s: ok\n  capabilities  %s\n  resources     %d MB, %g CPU, %d processes, %d min per run\n",
		m.ID, m.Version, caps, m.Run.MemoryMB, m.Run.CPUs, m.Run.Pids, m.Run.TimeoutMin)
	if m.Permissions.StorageKB > 0 {
		fmt.Printf("  storage       %d KB\n", m.Permissions.StorageKB)
	}
	for _, s := range m.Permissions.Schedule {
		fmt.Printf("  schedule      %s\n", s)
	}
	return nil
}

func build(manifestPath, executablePath, arch, out string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	exe, err := os.ReadFile(executablePath)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	res, err := pack.Build(data, exe, arch, &buf)
	if err != nil {
		return err
	}
	if out == "" {
		out = res.ID + "-" + res.Version + ".kagent"
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s %s\n  image  %s\n  digest %s\n  size   %d bytes\n", out, res.Version, res.Image, res.Digest, buf.Len())
	return nil
}
