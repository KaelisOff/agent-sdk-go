// Package manifest reads and checks agent manifests (manifest.toml).
//
// The rules are the box's: a manifest accepted here is accepted by a box
// of the same protocol version, and the limits below are the platform's.
// Unknown keys are rejected, so that a manifest asking for a capability
// a box does not know is never installed as if it asked for less.
package manifest

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Capabilities an agent can ask for.
const (
	CapModel    = "model"    // ask the box's model
	CapStorage  = "storage"  // private key-value storage
	CapSchedule = "schedule" // run at the times declared in the manifest
	CapNotify   = "notify"   // send notifications to the owner and admins
	CapApproval = "approval" // ask the owner or an admin for approval
)

// Platform limits: a manifest may ask for less, never for more.
const (
	MaxMemoryMB    = 512
	MaxCPUs        = 2.0
	MaxPids        = 128
	MaxTimeoutMin  = 30
	MaxStorageKB   = 16 * 1024
	MaxSchedules   = 8
	DefaultMemory  = 128
	DefaultCPUs    = 1.0
	DefaultPids    = 64
	DefaultTimeout = 10
)

// Manifest describes an agent.
type Manifest struct {
	// ID is a reverse-DNS identifier, unique in the catalog: a domain or
	// a GitHub account you control (io.github.<account>.<agent>).
	ID          string `toml:"id" json:"id"`
	Name        string `toml:"name" json:"name"`
	Version     string `toml:"version" json:"version"`
	Description string `toml:"description" json:"description"`
	Author      string `toml:"author" json:"author"`
	// License is required: agents are open source (SPDX identifier).
	License string `toml:"license" json:"license"`
	// Source is the public repository the package is built from.
	Source string `toml:"source" json:"source"`

	Run         Run         `toml:"run" json:"run"`
	Permissions Permissions `toml:"permissions" json:"permissions"`
}

// Run holds the resources an agent asks for at each run.
type Run struct {
	MemoryMB   int     `toml:"memory_mb" json:"memory_mb"`
	CPUs       float64 `toml:"cpus" json:"cpus"`
	Pids       int     `toml:"pids" json:"pids"`
	TimeoutMin int     `toml:"timeout_min" json:"timeout_min"`
}

// Permissions are the capabilities asked for.
type Permissions struct {
	Model    bool `toml:"model" json:"model,omitempty"`
	Notify   bool `toml:"notify" json:"notify,omitempty"`
	Approval bool `toml:"approval" json:"approval,omitempty"`
	// StorageKB > 0 asks for private storage of that size.
	StorageKB int `toml:"storage_kb" json:"storage_kb,omitempty"`
	// Schedule lists cron expressions (minute hour day month weekday),
	// in the box's time zone.
	Schedule []string `toml:"schedule" json:"schedule,omitempty"`
}

// Capabilities returns the capabilities asked for, in a stable order.
func (p Permissions) Capabilities() []string {
	var out []string
	if p.Model {
		out = append(out, CapModel)
	}
	if p.StorageKB > 0 {
		out = append(out, CapStorage)
	}
	if len(p.Schedule) > 0 {
		out = append(out, CapSchedule)
	}
	if p.Notify {
		out = append(out, CapNotify)
	}
	if p.Approval {
		out = append(out, CapApproval)
	}
	return out
}

// Has reports whether capability c is asked for.
func (p Permissions) Has(c string) bool { return slices.Contains(p.Capabilities(), c) }

var (
	idRe      = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*){2,6}$`)
	versionRe = regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,4}$`)
)

// ErrInvalid is wrapped by every manifest error.
var ErrInvalid = errors.New("invalid manifest")

// Parse decodes and checks a manifest, filling in the default resources.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	md, err := toml.Decode(string(data), &m)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return Manifest{}, fmt.Errorf("%w: unknown key %s", ErrInvalid, u[0])
	}
	if err := m.check(); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return m, nil
}

func (m *Manifest) check() error {
	switch {
	case !idRe.MatchString(m.ID) || len(m.ID) > 100:
		return errors.New("id must be reverse-DNS in lower case, e.g. io.github.account.agent")
	case !versionRe.MatchString(m.Version):
		return errors.New("version must be MAJOR.MINOR.PATCH")
	case strings.TrimSpace(m.Name) == "" || len(m.Name) > 60:
		return errors.New("name must be 1 to 60 characters")
	case len(m.Description) > 2000:
		return errors.New("description is too long")
	case m.License == "" || m.Source == "":
		return errors.New("license and source are required (agents are open source)")
	}
	r := &m.Run
	if r.MemoryMB == 0 {
		r.MemoryMB = DefaultMemory
	}
	if r.CPUs == 0 {
		r.CPUs = DefaultCPUs
	}
	if r.Pids == 0 {
		r.Pids = DefaultPids
	}
	if r.TimeoutMin == 0 {
		r.TimeoutMin = DefaultTimeout
	}
	switch {
	case r.MemoryMB < 16 || r.MemoryMB > MaxMemoryMB:
		return fmt.Errorf("run.memory_mb must be 16 to %d", MaxMemoryMB)
	case r.CPUs < 0.1 || r.CPUs > MaxCPUs:
		return fmt.Errorf("run.cpus must be 0.1 to %g", MaxCPUs)
	case r.Pids < 4 || r.Pids > MaxPids:
		return fmt.Errorf("run.pids must be 4 to %d", MaxPids)
	case r.TimeoutMin < 1 || r.TimeoutMin > MaxTimeoutMin:
		return fmt.Errorf("run.timeout_min must be 1 to %d", MaxTimeoutMin)
	}
	p := m.Permissions
	if p.StorageKB < 0 || p.StorageKB > MaxStorageKB {
		return fmt.Errorf("permissions.storage_kb must be 0 to %d", MaxStorageKB)
	}
	if len(p.Schedule) > MaxSchedules {
		return fmt.Errorf("at most %d schedules", MaxSchedules)
	}
	for _, s := range p.Schedule {
		if _, err := ParseSchedule(s); err != nil {
			return err
		}
	}
	return nil
}

// Image is the name a box gives the agent's container image.
func (m Manifest) Image() string { return "localhost/kaelis-agent/" + m.ID + ":" + m.Version }
