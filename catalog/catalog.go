// Package catalog fetches the catalogs of OKDP packages: git repositories
// holding one directory per package version, the packages okdp-compiler
// compiles, and the contracts their connections follow, parts of the tree
// tree.Load reads, which okdp-compiler indexes
// (Catalogs.Index):
//
//	catalog/<service>/<version>/        one package version (Chart.yaml, okdp.yaml, values.cel.yaml...)
//	contracts/<contract>.schema.json    the contracts of the catalog's packages
//
// The platform names its catalogs in the catalogs.yaml of its tree, whose
// schema is okdp-compiler's (treeedit.CatalogsFile,
// treeedit.CatalogRepositories):
//
//	repositories:
//	  - {name: platform-charts, url: https://github.com/OKDP/platform-charts.git, ref: main}
//
// A catalog is fetched at the repository's ref (Loader).
package catalog

import (
	"github.com/okdp/okdp-gitops/tree"

	"io/fs"

	compiler "github.com/okdp/okdp-compiler"
)

// Catalog is one catalog repository, its files at its ref.
type Catalog struct {
	Name string
	// Revision is the commit read ("" for a local directory).
	Revision string
	FS       fs.FS
}

// Catalogs are the catalogs of a platform, in the order of catalogs.yaml.
type Catalogs struct {
	list []Catalog
}

// New gathers catalogs.
func New(list ...Catalog) *Catalogs { return &Catalogs{list: list} }

// List returns the catalogs.
func (c *Catalogs) List() []Catalog { return append([]Catalog(nil), c.list...) }

// Index indexes the catalogs with okdp-compiler (compiler.LoadCatalog):
// their services and versions, the files of a package, the contracts, as
// a compile of the tree reads them.
func (c *Catalogs) Index() (*compiler.Catalog, error) {
	repos := make([]tree.Repository, 0, len(c.list))
	for _, cat := range c.list {
		repos = append(repos, tree.Repository{Name: cat.Name, FS: cat.FS, Role: tree.RoleCatalog})
	}
	return tree.LoadCatalog(repos)
}
