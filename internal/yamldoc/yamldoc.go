// Package yamldoc reads one YAML document as Helm and the Kubernetes API
// read it (chart.ParseValues: YAML 1.1 booleans, string keys, every number
// a float64), whatever its root, for the code that looks into a document
// of a chart. go.yaml.in/yaml/v3 reads the root's kind only: alone it reads
// y, yes, on... as strings, and refuses a duplicate key, what a Helm
// release takes.
package yamldoc

import (
	"github.com/okdp/okdp-compiler/chart"
	"go.yaml.in/yaml/v3"
)

// Decode: the document, a map, an empty one for a document without
// content, a []any for a list, nil for a scalar. An error when it is not
// YAML, or a map that is not read as JSON (a key that is a map or a list).
func Decode(doc []byte) (any, error) {
	m, err := chart.ParseValues(doc)
	if err == nil {
		return m, nil
	}
	// What ParseValues refused: a document that is not a map, or not YAML.
	var root yaml.Node
	if yaml.Unmarshal(doc, &root) != nil || len(root.Content) != 1 {
		return nil, err
	}
	switch root.Content[0].Kind {
	case yaml.MappingNode:
		return nil, err
	case yaml.SequenceNode:
		// A list goes back through ParseValues under a key: its scalars are
		// written back as they are, each read as in a map.
		b, merr := yaml.Marshal(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "list"}, root.Content[0]}})
		if merr != nil {
			return nil, merr
		}
		m, err := chart.ParseValues(b)
		if err != nil {
			return nil, err
		}
		return m["list"], nil
	}
	return nil, nil
}
