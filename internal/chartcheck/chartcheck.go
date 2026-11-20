// Package chartcheck checks the final values of a compile against the
// upstream charts that render them, which the compiler does not read: the
// values Helm's templates would see (the final values coalesced over the
// chart's values.yaml, chartutil.CoalesceValues) against the chart's
// values.schema.json, and the lists an instance appended to that leave
// out items of the chart's default list. The charts come from a Source,
// fetched once per build (Charts).
//
// Only the chart's own values.yaml and values.schema.json are read, not
// those of its subcharts. The schema is compiled by okdp-compiler's
// chart.ParseSchema, which loads no $ref from a URL or a file: Helm's own
// validation (chartutil.ValidateAgainstSchema) would fetch them.
package chartcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"reflect"
	"slices"
	"sync"

	helmchart "helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/chart"
	"github.com/okdp/okdp-compiler/value"

	"github.com/okdp/okdp-gitops/internal/chartdir"
)

// M: a values map.
type M = value.M

// utf8BOM is the byte order mark Helm's loader strips from a chart's files.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Source is where a build reads the upstream charts from: a
// chartcache.Cache, which pulls what it lacks, or Static, a directory
// filled already. Its charts are unpacked at chartdir.Of. Safe for
// concurrent use.
type Source interface {
	// Get makes the chart of ref readable in FS, pulled first when it must
	// be. Its error says why the chart cannot be read.
	Get(ctx context.Context, ref chart.Ref) error
	// FS gives the charts, each at its chartdir.Of.
	FS() fs.FS
}

// Static is a chart cache filled already, okdp-compile's --charts: it
// pulls nothing. Get says nothing of a chart it lacks: its check fails,
// with the command that pulls it (chartdir.Pull).
func Static(fsys fs.FS) Source { return static{fsys} }

type static struct{ fsys fs.FS }

func (s static) Get(context.Context, chart.Ref) error { return nil }
func (s static) FS() fs.FS                            { return s.fsys }

// Charts holds the upstream charts of one build: each fetched once from
// its source, the first time a compile names it (Fetch), each read once
// and its schema compiled once. It records why a chart could not be
// fetched: the keys it renders are left unchecked (Failures). It is safe
// for concurrent use, and valid for one build: it keeps what it has read.
type Charts struct {
	src  Source
	once sync.Once
	fsys fs.FS

	mu      sync.Mutex
	fetched map[chart.Ref]error // Get's answer for each chart asked, nil when it can be read
	read    map[chart.Ref]*upstream
}

// New returns the charts of a build, read from src; nil for a nil src: no
// chart fetched, none checked.
func New(src Source) *Charts {
	if src == nil {
		return nil
	}
	return &Charts{src: src, fetched: map[chart.Ref]error{}, read: map[chart.Ref]*upstream{}}
}

// FS returns the charts, each at its chartdir.Of: the source's FS, opened
// once for the build; nil for nil charts.
func (c *Charts) FS() fs.FS {
	if c == nil {
		return nil
	}
	c.once.Do(func() { c.fsys = c.src.FS() })
	return c.fsys
}

// Fetch gets each chart of refs from the source, once per build, and
// records why one cannot be fetched (Failures). It returns ctx's error
// when ctx ends, which it does not record: the next compile that names
// the chart asks again.
func (c *Charts) Fetch(ctx context.Context, refs []chart.Ref) error {
	if c == nil {
		return nil
	}
	for _, ref := range refs {
		c.mu.Lock()
		_, done := c.fetched[ref]
		c.mu.Unlock()
		if done {
			continue
		}
		err := c.src.Get(ctx, ref)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		c.fetched[ref] = err
		c.mu.Unlock()
	}
	return nil
}

// Failed says why the chart ref could not be fetched; nil when it was,
// or was not asked for.
func (c *Charts) Failed(ref chart.Ref) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetched[ref]
}

// Failures says why each chart that could not be fetched could not be,
// by chart; nil when every chart was fetched.
func (c *Charts) Failures() map[chart.Ref]error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out map[chart.Ref]error
	for ref, err := range c.fetched {
		if err != nil {
			if out == nil {
				out = map[chart.Ref]error{}
			}
			out[ref] = err
		}
	}
	return out
}

// Check checks the final values of a compile against the chart of each
// key, the keys in key order: the schema violations of every key are
// reported together, as Helm reports them; it returns a warning for each
// appended list (FinalValuesOf.Appended) where the chart's defaults hold
// items the final list lacks. The chart of each key is fetched first
// (Fetch); a chart that could not be fetched leaves its keys unchecked.
// A chart the source lacks (Static) fails with the command that pulls it.
// Nil charts check nothing.
func (c *Charts) Check(ctx context.Context, final compiler.FinalValuesOf) ([]string, error) {
	if c == nil {
		return nil, nil
	}
	keys := slices.Sorted(maps.Keys(final.Values))
	var refs []chart.Ref
	for _, key := range keys {
		refs = append(refs, final.Charts[key])
	}
	if err := c.Fetch(ctx, refs); err != nil {
		return nil, err
	}
	violations := map[string]string{}
	var warnings []string
	for _, key := range keys {
		ref := final.Charts[key]
		if c.Failed(ref) != nil {
			continue
		}
		u, err := c.get(ref)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		bad, w, err := u.check(key, ref, final.Values[key], final.Appended[key])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		violations[key+" ("+ref.ID()+")"] = bad
		warnings = append(warnings, w...)
	}
	if err := chart.SchemaError("compiled values", violations); err != nil {
		return nil, err
	}
	return warnings, nil
}

// upstream is what a check reads of an upstream chart. Its schema is
// compiled once, when a check first needs it.
type upstream struct {
	entry    string // its directory in the chart cache, for the errors
	defaults M      // its values.yaml, empty when the chart has none
	schema   []byte // its values.schema.json, nil when the chart has none
	once     sync.Once
	compiled *chart.Schema
	err      error
}

// get returns the chart ref, read once from its directory of the cache.
// A chart missing from the cache fails with the command that pulls it,
// and the next call looks for it again.
func (c *Charts) get(ref chart.Ref) (*upstream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if u, ok := c.read[ref]; ok {
		return u, nil
	}
	entry, err := chartdir.Of(ref)
	if err != nil {
		return nil, err
	}
	files, err := readEntry(c.FS(), entry)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("chart %s is not in the chart cache: %s", ref.ID(), chartdir.Pull(ref))
	}
	if err != nil {
		return nil, err
	}
	u := &upstream{entry: entry, defaults: M{}, schema: files["values.schema.json"]}
	if b, ok := files["values.yaml"]; ok {
		if u.defaults, err = chart.ParseValues(b); err != nil {
			return nil, fmt.Errorf("%s/values.yaml: %w", entry, err)
		}
	}
	c.read[ref] = u
	return u, nil
}

// readEntry reads values.yaml and values.schema.json from name, an
// unpacked chart. It returns the files the chart has, without their byte
// order mark, and fs.ErrNotExist when the directory does not exist.
func readEntry(fsys fs.FS, name string) (map[string][]byte, error) {
	if _, err := fs.Stat(fsys, name); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, file := range []string{"values.yaml", "values.schema.json"} {
		b, err := fs.ReadFile(fsys, path.Join(name, file))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files[file] = bytes.TrimPrefix(b, utf8BOM)
	}
	return files, nil
}

// check checks the final values of key against the chart u. It returns
// the violations of the chart's schema ("" when the values hold), and a
// warning for each appended list where the chart's defaults hold items
// the final list lacks: Helm replaces the chart's list, and append extends
// the package's list only.
func (u *upstream) check(key string, ref chart.Ref, final M, appended [][]string) (string, []string, error) {
	var warnings []string
	for _, p := range appended {
		defaults, _ := valueAt(u.defaults, p).([]any)
		list, _ := valueAt(final, p).([]any)
		lost := 0
		for _, item := range defaults {
			if !slices.ContainsFunc(list, func(x any) bool { return reflect.DeepEqual(x, item) }) {
				lost++
			}
		}
		if lost == 0 {
			continue
		}
		items := fmt.Sprintf("%d of its %d items", lost, len(defaults))
		if lost == len(defaults) {
			items = "all its items"
		}
		warnings = append(warnings, fmt.Sprintf("%s: appended to the package's list, not to the default list of %s, which Helm replaces: the final list lacks %s; restate them in the package's values of %s (its defaults or values file) to keep them", upstreamAt(key, chart.FormatPath(p)), ref.ID(), items, key))
	}
	if u.schema == nil {
		return "", warnings, nil
	}
	// The chart is checked as a subchart of the umbrella: Helm gives it a
	// global, empty when the values have none.
	values, err := Coalesce(u.defaults, final)
	if err != nil {
		return "", nil, err
	}
	if _, ok := values["global"]; !ok {
		values["global"] = M{}
	}
	name := u.entry + "/values.schema.json"
	u.once.Do(func() { u.compiled, u.err = chart.ParseSchema(name, u.schema) })
	if u.err != nil {
		return "", nil, u.err
	}
	v, err := u.compiled.Validate(values)
	return v, warnings, err
}

// Coalesce returns the values a chart's templates see: values over the
// chart's defaults, as Helm coalesces them (chartutil.CoalesceValues) for
// a chart without subcharts. Maps merge key by key; a null in values
// deletes the default. It changes neither argument.
func Coalesce(defaults, values M) (M, error) {
	c := &helmchart.Chart{Metadata: &helmchart.Metadata{Name: "chart"}, Values: defaults}
	out, err := chartutil.CoalesceValues(c, values)
	if err != nil {
		return nil, err
	}
	return M(out), nil
}

func valueAt(v any, keys []string) any {
	for _, k := range keys {
		m, ok := v.(M)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

// upstreamAt names the path p of the instance's upstream values of key,
// as the compiler names them in its errors. A path that starts with a key
// in brackets (chart.FormatPath) is joined without a dot.
func upstreamAt(key, p string) string {
	switch {
	case p == "":
		return "upstream." + key
	case p[0] == '[':
		return "upstream." + key + p
	}
	return "upstream." + key + "." + p
}
