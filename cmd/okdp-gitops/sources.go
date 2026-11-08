package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/okdp/okdp-gitops/treeedit"
	"go.yaml.in/yaml/v3"

	"github.com/okdp/okdp-gitops/catalog"
	"github.com/okdp/okdp-gitops/gitops"
	"github.com/okdp/okdp-gitops/internal/buildinfo"
)

// The record of the sources: the output's gitops.SourcesFile, which a
// compile of Git repositories writes beside compiled/, names what the
// output is the compile of: the okdp-gitops version, a digest of the
// topology (its locations, the path prefix, the chart checks), a digest of
// the output itself, the revision of each repository of the sources and
// of each catalog. A run reads the output first: when its record holds,
// every revision the same as the remote's (a list of the refs, no fetch),
// it stops there, before the sources, the catalogs and the compile.
//
// A compile that keeps a project writes no record, the old one left as it
// was: the output it wrote does not match it any more, or the sources it
// names moved, and the next run compiles again. A compile of a local
// directory, or of a local catalog (--catalog), removes the record: the
// output is not the compile of revisions any more.

// sources is the record of gitops.SourcesFile.
type sources struct {
	Version      string     `yaml:"okdp-gitops"`
	Topology     string     `yaml:"topology"`
	Output       string     `yaml:"output"`
	Repositories []revision `yaml:"repositories"`
	Catalogs     []revision `yaml:"catalogs,omitempty"`
}

// readCatalog is a catalog a compile read: its repository of
// treeedit.CatalogsFile, and what was read of it.
type readCatalog struct {
	repo treeedit.CatalogRepository
	cat  catalog.Catalog
}

// revision is a repository of the sources (its branch) or a catalog (its
// name and ref), and the commit compiled.
type revision struct {
	Name     string `yaml:"name,omitempty"`
	URL      string `yaml:"url"`
	Branch   string `yaml:"branch,omitempty"`
	Ref      string `yaml:"ref,omitempty"`
	Revision string `yaml:"revision"`
}

// recordPath is the path of the record in the output's repository.
func (c compilation) recordPath() string {
	return path.Join(".", c.topology.Output.Path, gitops.SourcesFile)
}

// topologyDigest is the digest of what, beside the sources, makes the
// output: the locations of the topology, the path prefix, the chart checks.
func (c compilation) topologyDigest() string {
	h := sha256.New()
	for _, l := range []gitops.Location{c.topology.Platform, c.topology.Projects, c.topology.Output} {
		_, _ = fmt.Fprintf(h, "%q %q %q\n", l.URL, l.Branch, l.Path)
	}
	_, _ = fmt.Fprintf(h, "prefix %q\ncharts %v\n", c.prefix, c.charts != nil)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// outputDigest is the digest of the files of the output, but the record.
func (c compilation) outputDigest(files map[string]string) string {
	record := c.recordPath()
	paths := make([]string, 0, len(files))
	for p := range files {
		if p != record {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		_, _ = fmt.Fprintf(h, "%q %d\n", p, len(files[p]))
		_, _ = h.Write([]byte(files[p]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// output returns the output's repository among those opened, nil when it
// is not opened yet.
func (c compilation) output(repos []*opened) *opened {
	for _, o := range repos {
		if o.loc.Repository() == c.topology.Output.Repository() {
			return o
		}
	}
	return nil
}

// record returns the record of a compile that read repos and the catalogs
// cats (their repositories and what was read of them), false when there is
// none to make: a local directory, a catalog read from one.
func (c compilation) record(repos []*opened, cats []readCatalog) (*sources, bool) {
	out := c.output(repos)
	if out == nil || out.git == nil {
		return nil, false
	}
	rec := &sources{Version: buildinfo.CurrentVersion(), Topology: c.topologyDigest(), Output: c.outputDigest(out.store.Files())}
	for _, o := range repos {
		if o == out {
			continue
		}
		if o.git == nil {
			return nil, false
		}
		rec.Repositories = append(rec.Repositories, revision{URL: o.loc.URL, Branch: o.loc.Branch, Revision: o.base})
	}
	sort.Slice(rec.Repositories, func(i, j int) bool {
		return rec.Repositories[i].URL+"#"+rec.Repositories[i].Branch < rec.Repositories[j].URL+"#"+rec.Repositories[j].Branch
	})
	for _, rc := range cats {
		if rc.cat.Revision == "" {
			return nil, false
		}
		rec.Catalogs = append(rec.Catalogs, revision{Name: rc.repo.Name, URL: rc.repo.URL, Ref: rc.repo.Ref, Revision: rc.cat.Revision})
	}
	return rec, true
}

// writeRecord writes the record of the compile into the output's store,
// or removes the output's when there is none to make; a compile that kept
// a project writes none, and leaves the output's as it is.
func (c compilation) writeRecord(ctx context.Context, repos []*opened, cats []readCatalog, kept int) error {
	out := c.output(repos)
	if out == nil {
		return nil
	}
	p := c.recordPath()
	rec, ok := c.record(repos, cats)
	if !ok {
		if _, has := out.store.Files()[p]; !has {
			return nil
		}
		_, err := out.store.Update(ctx, gitops.Commit{Subject: "okdp: compile"}, func(tx gitops.Tx) error { return tx.Remove(p) })
		return err
	}
	if kept > 0 {
		return nil
	}
	data, err := treeedit.MarshalYAML(rec)
	if err != nil {
		return err
	}
	data = append([]byte(gitops.GeneratedHeader+"\n# The sources this output is the compile of (okdp-gitops compile).\n"), data...)
	_, err = out.store.Update(ctx, gitops.Commit{Subject: "okdp: compile"}, func(tx gitops.Tx) error { return tx.WriteFile(p, data) })
	return err
}

// upToDate reads the output's repository, through repositories, and tells
// whether its record holds: the same okdp-gitops, topology and output,
// every repository of the sources and every catalog at the revision it
// names. False when the output has no record, or a revision cannot be
// listed: the compile tells why.
func (c compilation) upToDate(ctx context.Context, repositories *gitops.Repositories, repos func() []*opened) bool {
	if c.check || !c.remote() {
		return false
	}
	if _, err := repositories.Store(c.topology.Output); err != nil {
		return false
	}
	out := c.output(repos())
	if out == nil || out.git == nil {
		return false
	}
	raw, ok := out.before[c.recordPath()]
	if !ok {
		return false
	}
	var rec sources
	if err := yaml.Unmarshal([]byte(raw), &rec); err != nil {
		return false
	}
	if version := buildinfo.CurrentVersion(); version == "dev" || rec.Version != version ||
		rec.Topology != c.topologyDigest() || rec.Output != c.outputDigest(out.before) {
		return false
	}
	// The platform's repository, and the projects' when it is one, are
	// recorded: the platform's revision holds projects.yaml, the other
	// repositories of the projects.
	for _, l := range []gitops.Location{c.topology.Platform, c.topology.Projects} {
		if l == c.topology.Projects && c.topology.Split() {
			continue
		}
		if !slicesContainsRepository(rec.Repositories, l) {
			return false
		}
	}
	for _, r := range rec.Repositories {
		auth, err := c.creds.For(r.URL)
		if err != nil {
			return false
		}
		refs, err := listRefs(ctx, r.URL, auth)
		if err != nil || refs["refs/heads/"+r.Branch] != r.Revision {
			return false
		}
	}
	catalogAuth := c.creds.Catalogs(c.topology.Platform.URL)
	for _, r := range rec.Catalogs {
		if _, local := catalog.LocalPath(r.URL); local || c.catalogs[r.Name] != "" {
			return false
		}
		if isCommit(r.Ref) {
			if r.Ref != r.Revision {
				return false
			}
			continue
		}
		auth, err := catalogAuth(r.URL)
		if err != nil {
			return false
		}
		refs, err := listRefs(ctx, r.URL, auth)
		if err != nil {
			return false
		}
		current, ok := refs["refs/heads/"+r.Ref]
		if !ok {
			if current, ok = refs["refs/tags/"+r.Ref+"^{}"]; !ok {
				current = refs["refs/tags/"+r.Ref]
			}
		}
		if current != r.Revision {
			return false
		}
	}
	return true
}

// remote reports whether every part of the topology is a Git repository.
func (c compilation) remote() bool {
	for _, l := range []gitops.Location{c.topology.Platform, c.topology.Projects, c.topology.Output} {
		if gitops.IsLocal(l.URL) {
			return false
		}
	}
	return true
}

func slicesContainsRepository(list []revision, l gitops.Location) bool {
	for _, r := range list {
		if r.URL == l.URL && r.Branch == l.Branch {
			return true
		}
	}
	return false
}

// isCommit tells a ref that is a full commit hash.
func isCommit(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	_, err := hex.DecodeString(ref)
	return err == nil
}

// listRefs lists the refs of a remote repository, by name, and the
// commits of its annotated tags (<tag>^{}): no fetch. An empty repository
// has none.
func listRefs(ctx context.Context, url string, auth transport.AuthMethod) (map[string]string, error) {
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{Name: "origin", URLs: []string{url}})
	refs, err := remote.ListContext(ctx, &git.ListOptions{Auth: auth, PeelingOption: git.AppendPeeled})
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(refs))
	for _, r := range refs {
		if name := r.Name().String(); strings.HasPrefix(name, "refs/") {
			out[name] = r.Hash().String()
		}
	}
	return out, nil
}

// keptSince returns the revision a kept project's output is the compile
// of: in the output's record as the run read it, which the last compile
// that wrote the project's output wrote (a compile that keeps a project
// leaves the record as it was), the revision of the repository of the
// sources that holds the project. False when the record names none: no
// record (a local directory, a catalog read from one, never compiled),
// a repository the record does not name (new since).
func (c compilation) keptSince(repos []*opened, projects *treeedit.ProjectsList, project string) (revision, bool) {
	out := c.output(repos)
	if out == nil {
		return revision{}, false
	}
	raw, ok := out.before[c.recordPath()]
	if !ok {
		return revision{}, false
	}
	var rec sources
	if err := yaml.Unmarshal([]byte(raw), &rec); err != nil {
		return revision{}, false
	}
	var entry *treeedit.ProjectEntry
	if c.topology.Split() && projects != nil {
		entry = projects.Entry(project)
	}
	loc, err := c.topology.Project(project, entry)
	if err != nil {
		return revision{}, false
	}
	for _, r := range rec.Repositories {
		if r.URL == loc.URL && r.Branch == loc.Branch {
			return r, true
		}
	}
	return revision{}, false
}
