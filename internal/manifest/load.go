package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Inventory struct {
	Root   string
	Policy Policy
	Teams  map[string]*Team
	Keys   []*Key
}

func Load(root string) (*Inventory, error) {
	inv := &Inventory{Root: root, Teams: map[string]*Team{}}
	var errs []error

	pol, err := LoadPolicy(filepath.Join(root, "policy.yaml"))
	if err != nil {
		errs = append(errs, err)
	}
	inv.Policy = *pol

	teamDirs, err := os.ReadDir(filepath.Join(root, "teams"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	for _, d := range teamDirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(root, "teams", d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, f := range files {
			if f.IsDir() || !isYAML(f.Name()) {
				continue
			}
			path := filepath.Join(dir, f.Name())
			rel := relPath(root, path)
			if f.Name() == "team.yaml" || f.Name() == "team.yml" {
				t := &Team{Dir: d.Name()}
				if err := decodeFile(path, t); err != nil {
					errs = append(errs, err)
					continue
				}
				inv.Teams[d.Name()] = t
				continue
			}
			k := &Key{Team: d.Name(), Path: rel}
			if err := decodeFile(path, k); err != nil {
				errs = append(errs, err)
				continue
			}
			inv.Keys = append(inv.Keys, k)
		}
	}
	sort.Slice(inv.Keys, func(i, j int) bool { return inv.Keys[i].Name < inv.Keys[j].Name })
	for _, k := range inv.Keys {
		if kp := inv.Policy.Kind(k.Kind); kp != nil {
			if k.Budget.Limit == 0 {
				k.Budget.Limit = kp.Budget.Limit
			}
			if k.Budget.Reset == "" {
				k.Budget.Reset = kp.Budget.Reset
			}
			if k.TTL.Duration == 0 {
				k.TTL = kp.TTL
			}
		}
		if k.Track == nil {
			t := inv.Policy.Providers[k.Provider].Options.TrackOnly
			k.Track = &t
		}
	}

	return inv, errors.Join(errs...)
}

func LoadPolicy(path string) (*Policy, error) {
	p := &Policy{}
	err := decodeFile(path, p)
	p.setDefaults()
	return p, err
}

func (inv *Inventory) Key(name string) *Key {
	for _, k := range inv.Keys {
		if k.Name == name {
			return k
		}
	}
	return nil
}

// TeamNames returns the team names in sorted order.
func (inv *Inventory) TeamNames() []string {
	names := make([]string, 0, len(inv.Teams))
	for n := range inv.Teams {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (p *Policy) setDefaults() {
	if p.State == "" {
		p.State = "vault://secret/orkeys/state"
	}
	if p.Naming == "" {
		p.Naming = defaultNaming
	}
	if len(p.Envs) == 0 {
		p.Envs = []string{"prod", "staging", "dev"}
	}
	if p.Secrets == "" {
		p.Secrets = "vault"
	}
	for _, kp := range p.Kinds {
		if kp == nil {
			continue
		}
		if kp.Source == "" {
			kp.Source = SourceManifest
		}
		if kp.Budget.Limit == 0 {
			kp.Budget.Limit = kp.MaxLimit
		}
		if kp.Budget.Reset == "" {
			kp.Budget.Reset = DefaultReset
		}
		if kp.Source == SourceManifest {
			if kp.RotateBefore.Duration == 0 {
				kp.RotateBefore.Duration = 3 * 24 * hour
			}
			if kp.RevokeGrace.Duration == 0 {
				kp.RevokeGrace.Duration = 24 * hour
			}
		}
	}
}

func (p *Policy) KubeContext(cluster string) string {
	if c, ok := p.Clusters[cluster]; ok && c.Context != "" {
		return c.Context
	}
	return cluster
}

func decodeFile(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func relPath(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}
