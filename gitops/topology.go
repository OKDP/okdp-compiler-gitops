package gitops

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/okdp/okdp-gitops/treeedit"
	"go.yaml.in/yaml/v3"
)

// ProjectPlaceholder stands for the project name in the url and the path of
// the projects of a topology.
const ProjectPlaceholder = "{project}"

// Location is where a part of the tree lives: a git repository, its
// branch and the directory of the repository holding the part.
type Location struct {
	URL    string `yaml:"url"`
	Branch string `yaml:"branch,omitempty"`
	Path   string `yaml:"path,omitempty"`
}

// Repository identifies the repository of a location: its url and branch.
func (l Location) Repository() string { return l.URL + "#" + l.Branch }

// ForgejoConfig lets the server create the repository of a new project
// (split topology), through the Forgejo API.
type ForgejoConfig struct {
	// URL of the Forgejo server (its API is <url>/api/v1).
	URL string `yaml:"url"`
	// Owner is the organization of the repositories.
	Owner string `yaml:"owner"`
	// TokenFile holds the API token, read at each use.
	TokenFile string `yaml:"tokenFile"`
}

// Topology says where each part of the tree lives (GITOPS_CONFIG,
// okdp-gitops --config):
//
//	platform: {url: <git url>, branch: main, path: ""}   # the directory of platform.yaml, catalogs.yaml (projects.yaml)
//	projects: {url: <git url, may contain {project}>, branch: main, path: "projects/{project}"}
//	output:   {url: <git url>, branch: main, path: ""}   # the directory of compiled/ (and argocd/root.yaml)
//	forgejo:  {url: ..., owner: okdp, tokenFile: /etc/okdp/forgejo/token}   # optional
//
// io: the platform and the projects in the repositories of the sources
// (one, or the platform's apart), the output in a repository of its own.
// Split: a repository per project, its url naming {project}; the projects
// are then listed in treeedit.ProjectsFile. The output's repository is never one of
// the sources' (Validate): people write the sources, the CI alone the
// output, which the engines deploy.
type Topology struct {
	Platform Location       `yaml:"platform"`
	Projects Location       `yaml:"projects"`
	Output   Location       `yaml:"output"`
	Forgejo  *ForgejoConfig `yaml:"forgejo,omitempty"`
}

// LoadTopology reads a topology file. A local directory url (okdp-gitops)
// relative to the file is made relative to the current directory.
func LoadTopology(file string) (*Topology, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	t, err := ParseTopology(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	base := path.Dir(strings.ReplaceAll(file, `\`, "/"))
	for _, l := range []*Location{&t.Platform, &t.Projects, &t.Output} {
		if IsLocal(l.URL) && !path.IsAbs(l.URL) {
			l.URL = path.Join(base, l.URL)
		}
	}
	return t, nil
}

// ParseTopology reads a topology, checks it and fills the defaults
// (branch main).
func ParseTopology(raw []byte) (*Topology, error) {
	var t Topology
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("invalid topology: %w", err)
	}
	for _, l := range []*Location{&t.Platform, &t.Projects, &t.Output} {
		l.URL = strings.TrimSpace(l.URL)
		l.Path = strings.Trim(strings.TrimSpace(l.Path), "/")
		if l.Branch == "" {
			l.Branch = "main"
		}
	}
	return &t, t.Validate()
}

// Validate refuses a topology the server cannot work with.
func (t *Topology) Validate() error {
	for _, part := range []struct {
		name string
		loc  Location
	}{{"platform", t.Platform}, {"projects", t.Projects}, {"output", t.Output}} {
		if part.loc.URL == "" {
			return fmt.Errorf("%s.url is required", part.name)
		}
		if part.name != "projects" && strings.Contains(part.loc.URL+part.loc.Path, ProjectPlaceholder) {
			return fmt.Errorf("%s: %s names a project's repository only", part.name, ProjectPlaceholder)
		}
		if _, err := cleanPath(strings.ReplaceAll(part.loc.Path, ProjectPlaceholder, "p")); err != nil {
			return fmt.Errorf("%s.path: %w", part.name, err)
		}
		if err := checkBranch(part.loc.Branch); err != nil {
			return fmt.Errorf("%s.%w", part.name, err)
		}
	}
	if !t.Split() {
		dir, base := path.Split(t.Projects.Path)
		if base != ProjectPlaceholder || strings.Contains(dir, ProjectPlaceholder) {
			return fmt.Errorf("projects.path %q: without %s in projects.url, the projects are the directories of one repository, the path must end with /%s", t.Projects.Path, ProjectPlaceholder, ProjectPlaceholder)
		}
	}
	for _, part := range []struct {
		name string
		loc  Location
	}{{"platform", t.Platform}, {"projects", t.Projects}} {
		if sameRepository(part.loc.URL, t.Output.URL) {
			return fmt.Errorf("output.url %q is the repository of the %s: the output is in a repository of its own, which the CI alone writes, never in one of the sources", t.Output.URL, part.name)
		}
	}
	if f := t.Forgejo; f != nil && (f.URL == "" || f.Owner == "" || f.TokenFile == "") {
		return errors.New("forgejo: url, owner and tokenFile are required")
	}
	return nil
}

// Split reports whether each project has a repository of its own: the
// projects url names {project}. Its projects are listed in treeedit.ProjectsFile.
func (t *Topology) Split() bool { return strings.Contains(t.Projects.URL, ProjectPlaceholder) }

// ProjectsListDir is the directory whose subdirectories are the projects,
// in the repository of the projects (a topology that is not split).
func (t *Topology) ProjectsListDir() string {
	dir, _ := path.Split(t.Projects.Path)
	return strings.TrimSuffix(dir, "/")
}

// Project returns the location of a project: the projects template, with
// what its entry of treeedit.ProjectsFile overrides, checked as the
// template is, the entry being the platform's repository content, not the
// operator's (CheckEntry).
func (t *Topology) Project(name string, entry *treeedit.ProjectEntry) (Location, error) {
	l := t.projectTemplate(name)
	if entry == nil {
		return l, nil
	}
	if err := t.CheckEntry(entry); err != nil {
		return Location{}, fmt.Errorf("project %s: %s: %w", name, treeedit.ProjectsFile, err)
	}
	if entry.URL != "" {
		l.URL = entry.URL
	}
	if entry.Branch != "" {
		l.Branch = entry.Branch
	}
	if entry.Path != nil {
		l.Path, _ = entryPath(*entry.Path)
	}
	return l, nil
}

// projectTemplate is the location of a project by the projects template.
func (t *Topology) projectTemplate(name string) Location {
	return Location{
		URL:    strings.ReplaceAll(t.Projects.URL, ProjectPlaceholder, name),
		Branch: t.Projects.Branch,
		Path:   strings.ReplaceAll(t.Projects.Path, ProjectPlaceholder, name),
	}
}

// CheckEntry refuses an entry of treeedit.ProjectsFile whose overrides
// would take the project's reads and writes, and the credentials, out of
// the topology:
//
//   - url: in a topology whose projects are Git repositories, a local
//     directory or a file:// repository is refused, a Git URL whose host
//     is not one of the platform's, the projects' or the output's
//     templates too: the credentials go to those hosts only;
//   - branch: a branch name Git accepts;
//   - path: a relative path in the repository, without "..".
func (t *Topology) CheckEntry(entry *treeedit.ProjectEntry) error {
	if entry.URL != "" {
		if err := t.checkEntryURL(entry.URL); err != nil {
			return fmt.Errorf("url %q: %w", entry.URL, err)
		}
	}
	if entry.Branch != "" {
		if err := checkBranch(entry.Branch); err != nil {
			return err
		}
	}
	if entry.Path != nil {
		if _, err := entryPath(*entry.Path); err != nil {
			return err
		}
	}
	return nil
}

// checkEntryURL refuses the url of an entry of treeedit.ProjectsFile that
// is local in a topology of Git repositories, or on another host than the
// templates'.
func (t *Topology) checkEntryURL(raw string) error {
	if IsLocalRepository(raw) {
		if IsLocalRepository(t.Projects.URL) {
			return nil
		}
		return errors.New("a local directory or a file:// repository, in a topology whose projects are Git repositories: an entry names a repository of the topology's hosts")
	}
	host := strings.ToLower(urlHost(raw))
	var hosts []string
	for _, l := range []Location{t.Platform, t.Projects, t.Output} {
		h := strings.ToLower(urlHost(strings.ReplaceAll(l.URL, ProjectPlaceholder, "p")))
		if h == "" || slices.Contains(hosts, h) {
			continue
		}
		if h == host {
			return nil
		}
		hosts = append(hosts, h)
	}
	if host == "" {
		return errors.New("names no host")
	}
	return fmt.Errorf("its host %s is not one of the topology's (%s): an entry names a repository of those hosts, the only ones the credentials go to", host, strings.Join(hosts, ", "))
}

// isLocalRepository tells a url that reads the local file system: a local
// directory (IsLocal) or a file:// repository.
func IsLocalRepository(raw string) bool {
	return IsLocal(raw) || strings.HasPrefix(strings.ToLower(raw), "file:")
}

// checkBranch refuses a branch name Git does not accept.
func checkBranch(branch string) error {
	if err := plumbing.NewBranchReferenceName(branch).Validate(); err != nil {
		return fmt.Errorf("branch %q is not a branch name", branch)
	}
	return nil
}

// entryPath is the path of an entry of treeedit.ProjectsFile in its
// repository, "" its root: relative, without "..", its trailing / left
// out.
func entryPath(raw string) (string, error) {
	if path.IsAbs(raw) || strings.HasPrefix(raw, `\`) {
		return "", fmt.Errorf("path %q is absolute: a path in the project's repository", raw)
	}
	if slices.Contains(strings.Split(strings.ReplaceAll(raw, `\`, "/"), "/"), "..") {
		return "", fmt.Errorf("path %q goes up (..): a path in the project's repository", raw)
	}
	p, err := cleanPath(strings.TrimSuffix(raw, "/"))
	if err != nil {
		return "", err
	}
	if p == "." {
		return "", nil
	}
	return p, nil
}

// ProjectRepository is the name of the repository of a new project of a
// split topology, the one a ForgejoClient creates in its owner: the last
// segment of the projects url with the project's name, without ".git"
// (https://git.example.org/okdp/okdp-project-{project}.git: the
// repository okdp-project-<project>).
func (t *Topology) ProjectRepository(name string) (string, error) {
	raw := t.projectTemplate(name).URL
	repo := raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		repo = u.Path
	} else if _, after, ok := strings.Cut(raw, ":"); ok && !IsLocal(raw) {
		// scp-like: [user@]host:path
		repo = after
	}
	repo = path.Base(strings.TrimSuffix(strings.Trim(repo, "/"), ".git"))
	if repo == "." || repo == "/" || repo == "" {
		return "", fmt.Errorf("projects.url %q names no repository for project %s", t.Projects.URL, name)
	}
	return repo, nil
}

// sameRepository reports whether two urls name one repository, whatever
// their scheme, user, port, case, trailing / or .git: the host and the
// path of the repository; a local directory by its clean path.
func sameRepository(a, b string) bool { return repositoryKey(a) == repositoryKey(b) }

func repositoryKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if IsLocal(raw) {
		return "dir:" + path.Clean(raw)
	}
	host, repo := "", raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host, repo = u.Hostname(), u.Path
	} else if before, after, ok := strings.Cut(raw, ":"); ok {
		// scp-like: [user@]host:path
		host, repo = before[strings.LastIndex(before, "@")+1:], after
	}
	repo = strings.TrimSuffix(strings.Trim(repo, "/"), ".git")
	return strings.ToLower(host + "/" + strings.Trim(repo, "/"))
}

// IsLocal tells a url that is a local directory (okdp-gitops): an absolute
// path, or a path relative to the topology file (./, ../ or a name without
// scheme and without host). scheme://... (file:// included) and the
// scp-like git@host:path are git repositories.
func IsLocal(url string) bool {
	if path.IsAbs(url) || strings.HasPrefix(url, ".") {
		return true
	}
	return !strings.Contains(url, "://") && !strings.Contains(strings.SplitN(url, "/", 2)[0], ":")
}
