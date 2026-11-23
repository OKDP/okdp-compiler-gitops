package helm

import (
	"strings"

	compiler "github.com/okdp/okdp-compiler"
	"github.com/okdp/okdp-compiler/value"
)

// What Argo CD is told of the objects Helm keeps: an object annotated
// keepAnnotation: keepValue (a claim's database) is kept alike by Argo CD,
// which reads none of Helm's annotations and would delete it, its data
// with it, when the render drops it or its Application goes. keepOptions
// are added to its syncOptionsAnnotation, unless it sets an option of
// that name.
const (
	keepAnnotation        = "helm.sh/resource-policy"
	keepValue             = "keep"
	syncOptionsAnnotation = "argocd.argoproj.io/sync-options"
)

// keepOptions: the Argo CD sync options of an object Helm keeps.
var keepOptions = []string{"Prune=false", "Delete=false"}

// kept: res with copies of its objects, Argo CD's sync options added to
// those Helm keeps (keepSynced). res itself is not changed: it stays as
// the compile returned it.
func kept(res *compiler.Result) *compiler.Result {
	if len(res.Objects) == 0 {
		return res
	}
	out := *res
	out.Objects = make([]map[string]any, len(res.Objects))
	for i, o := range res.Objects {
		out.Objects[i] = value.AsMap(value.Copy(o))
	}
	keepSynced(out.Objects)
	return &out
}

// keepSynced adds keepOptions to the syncOptionsAnnotation of each object
// that Helm keeps, and of the items of a List, in place: the caller gives
// copies. It leaves an option the object already sets, whatever its
// value.
func keepSynced(objects []map[string]any) {
	for _, o := range objects {
		meta, _ := o["metadata"].(map[string]any)
		if annotations, ok := meta["annotations"].(map[string]any); ok && helmKeeps(annotations) {
			var options []string
			if s, ok := annotations[syncOptionsAnnotation].(string); ok && strings.TrimSpace(s) != "" {
				options = strings.Split(s, ",")
			}
			set := map[string]bool{}
			for _, opt := range options {
				name, _, _ := strings.Cut(strings.TrimSpace(opt), "=")
				set[name] = true
			}
			for _, opt := range keepOptions {
				if name, _, _ := strings.Cut(opt, "="); !set[name] {
					options = append(options, opt)
				}
			}
			annotations[syncOptionsAnnotation] = strings.Join(options, ",")
		}
		var items []map[string]any
		for _, i := range value.AsList(o["items"]) {
			if m, ok := i.(map[string]any); ok {
				items = append(items, m)
			}
		}
		keepSynced(items)
	}
}

// helmKeeps reports whether annotations say Helm keeps the object. It
// reads the annotation as Helm does: trimmed, in any case.
func helmKeeps(annotations map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(value.Str(annotations[keepAnnotation])), keepValue)
}
