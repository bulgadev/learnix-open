// Package elements contains the small, validated data model used by the
// learning-element renderers. It deliberately contains no HTML or executable
// content: the browser decides how a validated element is displayed.
package elements

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	TableType   = "table"
	MindMapType = "mind_map"

	MaxElements = 4
	MaxColumns  = 12
	MaxRows     = 80
	MaxNodes    = 80
	MaxText     = 1000
	MaxTitle    = 180
)

// Element is a renderer-neutral learning artifact. Table fields are used for
// table elements and RootID/Nodes for mind maps.
type Element struct {
	Type    string        `json:"type"`
	Title   string        `json:"title,omitempty"`
	Caption string        `json:"caption,omitempty"`
	Columns []string      `json:"columns,omitempty"`
	Rows    [][]string    `json:"rows,omitempty"`
	RootID  string        `json:"root_id,omitempty"`
	Nodes   []MindMapNode `json:"nodes,omitempty"`
}

// UnmarshalJSON accepts scalar JSON values in table cells and converts them
// to the renderer's text-only representation. Models commonly emit numeric
// table data as JSON numbers even when the surrounding prompt asks for text.
func (e *Element) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type    string            `json:"type"`
		Title   string            `json:"title"`
		Caption string            `json:"caption"`
		Columns []json.RawMessage `json:"columns"`
		Rows    []json.RawMessage `json:"rows"`
		RootID  string            `json:"root_id"`
		Nodes   []MindMapNode     `json:"nodes"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	columns, err := decodeTextValues(wire.Columns, "column")
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(wire.Rows))
	for ri, rawRow := range wire.Rows {
		var rawCells []json.RawMessage
		if err := json.Unmarshal(rawRow, &rawCells); err != nil {
			return fmt.Errorf("row %d must be an array: %w", ri+1, err)
		}
		cells, err := decodeTextValues(rawCells, fmt.Sprintf("cell in row %d", ri+1))
		if err != nil {
			return err
		}
		rows = append(rows, cells)
	}

	*e = Element{
		Type:    wire.Type,
		Title:   wire.Title,
		Caption: wire.Caption,
		Columns: columns,
		Rows:    rows,
		RootID:  wire.RootID,
		Nodes:   wire.Nodes,
	}
	return nil
}

func decodeTextValues(rawValues []json.RawMessage, name string) ([]string, error) {
	values := make([]string, 0, len(rawValues))
	for i, raw := range rawValues {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%s %d is invalid: %w", name, i+1, err)
		}
		switch typed := value.(type) {
		case nil:
			values = append(values, "")
		case string:
			values = append(values, typed)
		case json.Number:
			values = append(values, typed.String())
		case bool:
			values = append(values, fmt.Sprint(typed))
		default:
			return nil, fmt.Errorf("%s %d must be a scalar text value", name, i+1)
		}
	}
	return values, nil
}

type MindMapNode struct {
	ID          string `json:"id"`
	ParentID    string `json:"parent_id,omitempty"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (e Element) Validate() error {
	if strings.TrimSpace(e.Title) != "" && utf8.RuneCountInString(e.Title) > MaxTitle {
		return fmt.Errorf("element title exceeds %d characters", MaxTitle)
	}
	if strings.TrimSpace(e.Caption) != "" && utf8.RuneCountInString(e.Caption) > MaxText {
		return fmt.Errorf("element caption exceeds %d characters", MaxText)
	}
	switch e.Type {
	case TableType:
		return validateTable(e)
	case MindMapType:
		return validateMindMap(e)
	default:
		return fmt.Errorf("unknown element type %q", e.Type)
	}
}

func validateText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be blank", name)
	}
	if utf8.RuneCountInString(value) > MaxText {
		return fmt.Errorf("%s exceeds %d characters", name, MaxText)
	}
	return nil
}

func validateCell(name, value string) error {
	if utf8.RuneCountInString(value) > MaxText {
		return fmt.Errorf("%s exceeds %d characters", name, MaxText)
	}
	return nil
}

func validateTable(e Element) error {
	if len(e.Columns) == 0 || len(e.Columns) > MaxColumns {
		return fmt.Errorf("table must have 1-%d columns", MaxColumns)
	}
	for i, column := range e.Columns {
		if err := validateText(fmt.Sprintf("column %d", i+1), column); err != nil {
			return err
		}
	}
	if len(e.Rows) == 0 || len(e.Rows) > MaxRows {
		return fmt.Errorf("table must have 1-%d rows", MaxRows)
	}
	for ri, row := range e.Rows {
		if len(row) != len(e.Columns) {
			return fmt.Errorf("table row %d has %d cells, expected %d", ri+1, len(row), len(e.Columns))
		}
		for ci, cell := range row {
			if err := validateCell(fmt.Sprintf("cell %d,%d", ri+1, ci+1), cell); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMindMap(e Element) error {
	if len(e.Nodes) == 0 || len(e.Nodes) > MaxNodes {
		return fmt.Errorf("mind map must have 1-%d nodes", MaxNodes)
	}
	ids := make(map[string]bool, len(e.Nodes))
	roots := 0
	for i, node := range e.Nodes {
		if err := validateText(fmt.Sprintf("node %d id", i+1), node.ID); err != nil {
			return err
		}
		if ids[node.ID] {
			return fmt.Errorf("duplicate mind-map node id %q", node.ID)
		}
		ids[node.ID] = true
		if err := validateText(fmt.Sprintf("node %d label", i+1), node.Label); err != nil {
			return err
		}
		if node.Description != "" {
			if err := validateText(fmt.Sprintf("node %d description", i+1), node.Description); err != nil {
				return err
			}
		}
		if node.ParentID == "" {
			roots++
		}
	}
	if roots != 1 {
		return fmt.Errorf("mind map must have exactly one root")
	}
	if e.RootID != "" && !ids[e.RootID] {
		return fmt.Errorf("mind-map root_id %q does not exist", e.RootID)
	}
	rootID := e.RootID
	if rootID == "" {
		for _, node := range e.Nodes {
			if node.ParentID == "" {
				rootID = node.ID
				break
			}
		}
	}
	children := make(map[string][]string, len(e.Nodes))
	for _, node := range e.Nodes {
		if node.ParentID != "" {
			if !ids[node.ParentID] {
				return fmt.Errorf("node %q references missing parent %q", node.ID, node.ParentID)
			}
			children[node.ParentID] = append(children[node.ParentID], node.ID)
		}
	}
	seen := make(map[string]bool, len(e.Nodes))
	var walk func(string) error
	walk = func(id string) error {
		if seen[id] {
			return fmt.Errorf("mind map contains a cycle or duplicate path at %q", id)
		}
		seen[id] = true
		for _, child := range children[id] {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(rootID); err != nil {
		return err
	}
	if len(seen) != len(e.Nodes) {
		return fmt.Errorf("mind map contains disconnected nodes")
	}
	return nil
}

func ValidateAll(list []Element) error {
	if len(list) > MaxElements {
		return fmt.Errorf("at most %d elements are allowed", MaxElements)
	}
	for i, e := range list {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("element %d: %w", i+1, err)
		}
	}
	return nil
}

func Decode(raw string) ([]Element, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var list []Element
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("decode elements: %w", err)
	}
	if err := ValidateAll(list); err != nil {
		return nil, err
	}
	return list, nil
}

func Encode(list []Element) (string, error) {
	if err := ValidateAll(list); err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", fmt.Errorf("encode elements: %w", err)
	}
	return string(b), nil
}
