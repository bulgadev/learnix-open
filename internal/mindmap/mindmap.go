// Package mindmap contains the study mind-map domain model.
//
// The package is deliberately independent from HTTP, templ, SQLite, and the
// existing learning-element renderer. A future handler can use Graph as its
// validated persistence boundary and choose any renderer on top of it.
package mindmap

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxNodes              = 256
	MaxDepth              = 64
	MaxIDRunes            = 64
	MaxLabelRunes         = 160
	MaxDescriptionRunes   = 2000
	MaxMetadataEntries    = 16
	MaxMetadataKeyRunes   = 64
	MaxMetadataValueRunes = 256
)

// Node is one concept in a study mind map. ParentID is empty only for the
// single root node. Metadata is intentionally open-ended for future note/app
// associations; the domain validates its shape but does not assign meanings.
type Node struct {
	ID          string            `json:"id"`
	ParentID    string            `json:"parent_id,omitempty"`
	Label       string            `json:"label"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Collapsed   bool              `json:"collapsed"`
}

// Graph is the complete persisted state for one study.
type Graph struct {
	RootID string `json:"root_id"`
	Nodes  []Node `json:"nodes"`
}

// OutlineItem is the flat, depth-aware projection used by text and accessible
// renderers. Descendants of a collapsed node are not included.
type OutlineItem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Depth       int    `json:"depth"`
	HasChildren bool   `json:"has_children"`
	Collapsed   bool   `json:"collapsed"`
}

var (
	ErrNotFound       = errors.New("mind map not found")
	ErrInvalidStudyID = errors.New("mind map study id must be positive")
)

// Validate checks all structural, textual, and size invariants of the graph.
func (g Graph) Validate() error {
	if len(g.Nodes) == 0 {
		return errors.New("mind map cannot be empty: add exactly one root node")
	}
	if len(g.Nodes) > MaxNodes {
		return fmt.Errorf("mind map has %d nodes; maximum is %d", len(g.Nodes), MaxNodes)
	}
	if err := validateID("root_id", g.RootID); err != nil {
		return err
	}

	nodes := make(map[string]Node, len(g.Nodes))
	children := make(map[string][]string, len(g.Nodes))
	rootCount := 0
	for i, node := range g.Nodes {
		if err := node.Validate(); err != nil {
			return fmt.Errorf("node %d: %w", i+1, err)
		}
		if _, exists := nodes[node.ID]; exists {
			return fmt.Errorf("duplicate mind-map node id %q", node.ID)
		}
		nodes[node.ID] = node
		if node.ParentID == "" {
			rootCount++
		} else {
			children[node.ParentID] = append(children[node.ParentID], node.ID)
		}
	}
	if rootCount != 1 {
		return fmt.Errorf("mind map must have exactly one root; found %d", rootCount)
	}
	root, exists := nodes[g.RootID]
	if !exists {
		return fmt.Errorf("mind-map root_id %q does not exist", g.RootID)
	}
	if root.ParentID != "" {
		return fmt.Errorf("mind-map root_id %q must not have a parent", g.RootID)
	}
	for _, node := range g.Nodes {
		if node.ParentID != "" {
			if _, exists := nodes[node.ParentID]; !exists {
				return fmt.Errorf("node %q references missing parent %q", node.ID, node.ParentID)
			}
		}
	}

	for parent := range children {
		sort.Strings(children[parent])
	}
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("mind map contains a cycle at node %q", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, childID := range children[id] {
			if err := visit(childID); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}

	reachable := make(map[string]bool, len(nodes))
	var walk func(string, int) error
	walk = func(id string, depth int) error {
		if depth > MaxDepth {
			return fmt.Errorf("mind map depth exceeds %d levels", MaxDepth)
		}
		reachable[id] = true
		for _, childID := range children[id] {
			if err := walk(childID, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(g.RootID, 0); err != nil {
		return err
	}
	if len(reachable) != len(nodes) {
		return errors.New("mind map contains disconnected nodes")
	}
	return nil
}

// Validate checks one node independently of its parent existing in a graph.
func (n Node) Validate() error {
	if err := validateID("node id", n.ID); err != nil {
		return err
	}
	if n.ParentID != "" {
		if err := validateID("parent_id", n.ParentID); err != nil {
			return err
		}
	}
	if err := validateText("label", n.Label, MaxLabelRunes, false); err != nil {
		return err
	}
	if n.Description != "" {
		if err := validateText("description", n.Description, MaxDescriptionRunes, true); err != nil {
			return err
		}
	}
	if len(n.Metadata) > MaxMetadataEntries {
		return fmt.Errorf("metadata has %d entries; maximum is %d", len(n.Metadata), MaxMetadataEntries)
	}
	for key, value := range n.Metadata {
		if err := validateMetadataKey(key); err != nil {
			return err
		}
		if err := validateText("metadata value for "+key, value, MaxMetadataValueRunes, true); err != nil {
			return err
		}
	}
	return nil
}

// New creates a graph from one root node.
func New(root Node) (Graph, error) {
	root.ParentID = ""
	graph := Graph{RootID: root.ID, Nodes: []Node{root}}
	if err := graph.Validate(); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

// Node returns a defensive copy of a node by ID.
func (g Graph) Node(id string) (Node, error) {
	if err := validateID("node id", id); err != nil {
		return Node{}, err
	}
	for _, node := range g.Nodes {
		if node.ID == id {
			return cloneNode(node), nil
		}
	}
	return Node{}, fmt.Errorf("node %q not found", id)
}

// ExpandNode returns a graph with the node's children visible in an outline.
func (g Graph) ExpandNode(id string) (Graph, error) {
	return g.setCollapsed(id, false)
}

// CollapseNode returns a graph with the node's descendants hidden in an outline.
func (g Graph) CollapseNode(id string) (Graph, error) {
	return g.setCollapsed(id, true)
}

func (g Graph) setCollapsed(id string, collapsed bool) (Graph, error) {
	if err := g.Validate(); err != nil {
		return Graph{}, err
	}
	if err := validateID("node id", id); err != nil {
		return Graph{}, err
	}
	result := g.clone()
	for i := range result.Nodes {
		if result.Nodes[i].ID == id {
			result.Nodes[i].Collapsed = collapsed
			return result, nil
		}
	}
	return Graph{}, fmt.Errorf("node %q not found", id)
}

// AddNode returns a graph with node appended under an existing parent.
func (g Graph) AddNode(node Node) (Graph, error) {
	if err := g.Validate(); err != nil {
		return Graph{}, err
	}
	if err := node.Validate(); err != nil {
		return Graph{}, fmt.Errorf("add node: %w", err)
	}
	if node.ParentID == "" {
		return Graph{}, errors.New("add node: parent_id is required; a graph can have only one root")
	}
	if _, err := g.Node(node.ID); err == nil {
		return Graph{}, fmt.Errorf("add node: node %q already exists", node.ID)
	}
	if _, err := g.Node(node.ParentID); err != nil {
		return Graph{}, fmt.Errorf("add node: parent %q not found", node.ParentID)
	}
	result := g.clone()
	result.Nodes = append(result.Nodes, cloneNode(node))
	if err := result.Validate(); err != nil {
		return Graph{}, err
	}
	return result, nil
}

// UpdateNode returns a graph with an existing node replaced by node.
func (g Graph) UpdateNode(node Node) (Graph, error) {
	if err := g.Validate(); err != nil {
		return Graph{}, err
	}
	if err := node.Validate(); err != nil {
		return Graph{}, fmt.Errorf("update node: %w", err)
	}
	result := g.clone()
	found := false
	for i := range result.Nodes {
		if result.Nodes[i].ID == node.ID {
			result.Nodes[i] = cloneNode(node)
			found = true
			break
		}
	}
	if !found {
		return Graph{}, fmt.Errorf("update node: node %q not found", node.ID)
	}
	if err := result.Validate(); err != nil {
		return Graph{}, err
	}
	return result, nil
}

// RemoveNode returns a graph without id and any of its descendants.
func (g Graph) RemoveNode(id string) (Graph, error) {
	if err := g.Validate(); err != nil {
		return Graph{}, err
	}
	if err := validateID("node id", id); err != nil {
		return Graph{}, err
	}
	if id == g.RootID {
		return Graph{}, errors.New("remove node: the root cannot be removed")
	}
	if _, err := g.Node(id); err != nil {
		return Graph{}, fmt.Errorf("remove node: %w", err)
	}
	children := g.children()
	removed := make(map[string]bool)
	var mark func(string)
	mark = func(current string) {
		if removed[current] {
			return
		}
		removed[current] = true
		for _, childID := range children[current] {
			mark(childID)
		}
	}
	mark(id)
	result := g.clone()
	kept := result.Nodes[:0]
	for _, node := range result.Nodes {
		if !removed[node.ID] {
			kept = append(kept, node)
		}
	}
	result.Nodes = kept
	if err := result.Validate(); err != nil {
		return Graph{}, err
	}
	return result, nil
}

// Outline returns the visible graph in deterministic depth-first order.
func (g Graph) Outline() ([]OutlineItem, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	byID := g.nodeIndex()
	children := g.children()
	items := make([]OutlineItem, 0, len(g.Nodes))
	var walk func(string, int)
	walk = func(id string, depth int) {
		node := byID[id]
		items = append(items, OutlineItem{
			ID:          node.ID,
			Label:       node.Label,
			Description: node.Description,
			Depth:       depth,
			HasChildren: len(children[id]) > 0,
			Collapsed:   node.Collapsed,
		})
		if node.Collapsed {
			return
		}
		for _, childID := range children[id] {
			walk(childID, depth+1)
		}
	}
	walk(g.RootID, 0)
	return items, nil
}

// OutlineText returns a compact Markdown-like outline containing visible labels.
func (g Graph) OutlineText() (string, error) {
	items, err := g.Outline()
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, strings.Repeat("  ", item.Depth)+"- "+item.Label)
	}
	return strings.Join(lines, "\n"), nil
}

// MarshalJSON emits validated, canonical JSON. Nodes are serialized in
// deterministic preorder and metadata keys are sorted by encoding/json.
func (g Graph) MarshalJSON() ([]byte, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	type graphWire struct {
		RootID string `json:"root_id"`
		Nodes  []Node `json:"nodes"`
	}
	return json.Marshal(graphWire{RootID: g.RootID, Nodes: g.canonicalNodes()})
}

// UnmarshalJSON decodes only valid graph states, so callers cannot accidentally
// place an invalid graph into a repository.
func (g *Graph) UnmarshalJSON(data []byte) error {
	if g == nil {
		return errors.New("decode mind map into nil graph")
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("decode mind map: empty JSON")
	}
	type graphWire struct {
		RootID string `json:"root_id"`
		Nodes  []Node `json:"nodes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire graphWire
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("decode mind map: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("decode mind map: trailing JSON data")
		}
		return fmt.Errorf("decode mind map: trailing JSON data: %w", err)
	}
	decoded := Graph{RootID: wire.RootID, Nodes: wire.Nodes}
	if err := decoded.Validate(); err != nil {
		return fmt.Errorf("decode mind map: %w", err)
	}
	*g = decoded
	return nil
}

// Encode serializes one validated graph using the stable JSON contract.
func Encode(g Graph) ([]byte, error) {
	return json.Marshal(g)
}

// Decode deserializes one validated graph. Empty input is invalid rather than
// silently becoming a missing map.
func Decode(data []byte) (Graph, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Graph{}, errors.New("decode mind map: empty JSON")
	}
	var graph Graph
	if err := json.Unmarshal(data, &graph); err != nil {
		return Graph{}, err
	}
	return graph, nil
}

func validateID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s cannot be blank", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if utf8.RuneCountInString(value) > MaxIDRunes {
		return fmt.Errorf("%s exceeds %d characters", name, MaxIDRunes)
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		if i > 0 && (r == '-' || r == '_' || r == '.' || r == ':') {
			continue
		}
		return fmt.Errorf("%s %q contains unsafe character %q", name, value, r)
	}
	return nil
}

func validateMetadataKey(key string) error {
	if err := validateID("metadata key", key); err != nil {
		return err
	}
	if utf8.RuneCountInString(key) > MaxMetadataKeyRunes {
		return fmt.Errorf("metadata key exceeds %d characters", MaxMetadataKeyRunes)
	}
	return nil
}

func validateText(name, value string, maxRunes int, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be blank", name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxRunes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control character %q", name, r)
		}
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s cannot be blank", name)
	}
	return nil
}

func (g Graph) nodeIndex() map[string]Node {
	byID := make(map[string]Node, len(g.Nodes))
	for _, node := range g.Nodes {
		byID[node.ID] = cloneNode(node)
	}
	return byID
}

func (g Graph) children() map[string][]string {
	children := make(map[string][]string, len(g.Nodes))
	for _, node := range g.Nodes {
		if node.ParentID != "" {
			children[node.ParentID] = append(children[node.ParentID], node.ID)
		}
	}
	for parent := range children {
		sort.Strings(children[parent])
	}
	return children
}

func (g Graph) canonicalNodes() []Node {
	byID := g.nodeIndex()
	children := g.children()
	ordered := make([]Node, 0, len(g.Nodes))
	var walk func(string)
	walk = func(id string) {
		ordered = append(ordered, byID[id])
		for _, childID := range children[id] {
			walk(childID)
		}
	}
	walk(g.RootID)
	return ordered
}

func (g Graph) clone() Graph {
	result := Graph{RootID: g.RootID, Nodes: make([]Node, len(g.Nodes))}
	for i, node := range g.Nodes {
		result.Nodes[i] = cloneNode(node)
	}
	return result
}

func cloneNode(node Node) Node {
	copy := node
	if node.Metadata != nil {
		copy.Metadata = make(map[string]string, len(node.Metadata))
		for key, value := range node.Metadata {
			copy.Metadata[key] = value
		}
	}
	return copy
}
