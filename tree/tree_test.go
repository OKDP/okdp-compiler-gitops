package tree

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	compiler "github.com/okdp/okdp-compiler"
)

// instance: an instance of namespace named name, its release
// <namespace>-<name>.
func instance(namespace, name string) compiler.Instance {
	return compiler.Instance{Name: name, Inputs: compiler.Inputs{Namespace: namespace, Release: namespace + "-" + name}}
}

// TestBuild: a provider's claims, by ID, each with its claimer; what it
// granted; a refusal and a problem of the graph at their instances.
func TestBuild(t *testing.T) {
	db := compiler.Output{Contract: "database", Name: "demo.analytics"}
	instances := []compiler.Instance{instance("data", "pg"), instance("demo", "reports"), instance("demo", "metrics"), instance("demo", "lost"), instance("demo", "broken")}
	outcomes := []compiler.Outcome{
		{Result: &compiler.Result{Namespace: "data", Release: "data-pg", Info: compiler.Info{Provides: []compiler.Output{db, {Contract: "database", Name: "demo.metrics.database"}}}}},
		{Result: &compiler.Result{Namespace: "demo", Release: "demo-reports"}, Claims: []compiler.Bound{{Field: "database", Contract: "database", ID: "demo.analytics", Namespace: "data", Release: "data-pg", Named: true}}},
		{Result: &compiler.Result{Namespace: "demo", Release: "demo-metrics"}, Claims: []compiler.Bound{{Field: "database", Contract: "database", ID: "demo.metrics.database", Namespace: "data", Release: "data-pg"}}},
		{Result: &compiler.Result{Namespace: "demo", Release: "demo-lost", Dependencies: []compiler.Dependency{{Instance: "demo-gone", Contract: "database", Field: "database", Output: "x"}}}},
		{Err: errors.New("refused")},
	}
	tr := build(instances, outcomes)

	pg := tr.Instance("data", "pg")
	var ids []string
	for _, p := range pg.Provided {
		ids = append(ids, p.ID+" by "+p.Claimer.Namespace()+"/"+p.Claimer.Name)
	}
	if want := []string{"demo.analytics by demo/reports", "demo.metrics.database by demo/metrics"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("provided: got %v, want %v", ids, want)
	}
	if got := pg.Granted("demo.analytics"); got == nil || !reflect.DeepEqual(*got, db) {
		t.Errorf("granted: got %v", got)
	}
	if pg.Granted("demo.other") != nil || tr.Instance("demo", "broken").Granted("demo.analytics") != nil {
		t.Error("granted what it publishes none of")
	}
	if tr.Graph != nil {
		t.Error("a graph with a problem")
	}
	if tr.Release("demo", "demo-lost") != tr.Instance("demo", "lost") || tr.Release("demo", "lost") != nil {
		t.Error("an instance by its release")
	}
	problems := tr.Problems()
	if len(problems) != 2 || !strings.HasPrefix(problems[0], `demo/lost: database: it references "demo-gone"`) || problems[1] != "demo/broken: refused" {
		t.Errorf("problems: %q", problems)
	}
	if tr.Instance("demo", "reports").Err() != nil {
		t.Error("an instance that deploys has a problem")
	}
}

// TestBuildGraph: the graph of a tree without problems.
func TestBuildGraph(t *testing.T) {
	tr := build([]compiler.Instance{instance("demo", "a")}, []compiler.Outcome{{Result: &compiler.Result{Namespace: "demo", Release: "demo-a"}}})
	if tr.Graph == nil || tr.Problems() != nil {
		t.Errorf("graph %v, problems %v", tr.Graph, tr.Problems())
	}
}
