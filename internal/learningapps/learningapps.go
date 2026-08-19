// Package learningapps defines the small, declarative contract used by safe
// browser learning apps. It contains no renderer and never executes app data.
package learningapps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	TextType       = "text"
	QuizType       = "quiz"
	FlashcardsType = "flashcards"
	ChoiceType     = "choice"
	SequenceType   = "sequence"
	ProgressType   = "progress"

	MaxPayloadBytes = 64 * 1024
	MaxJSONDepth    = 8
	MaxModules      = 24
	MaxModuleParams = 16
	MaxStateFields  = 32
	MaxListItems    = 24
	MaxTextLength   = 4_000
	MaxIDLength     = 64
	MaxTitleLength  = 180
	MaxTags         = 12
	MaxInteractions = 100
	MaxDurationMS   = 15 * 60 * 1_000
	MinDurationMS   = 1_000
)

var (
	ErrPayloadTooLarge = errors.New("learning app payload exceeds the size limit")
	ErrJSONTooDeep     = errors.New("learning app JSON structure is too deep")
	ErrUnknownModule   = errors.New("learning app module type is not allowlisted")
	ErrUnsafeContent   = errors.New("learning app contains unsafe content")

	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	stateKeyPattern   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
)

// App is the complete JSON document accepted by the learning-app runtime.
// Params and InitialState deliberately use interface values so callers can
// build JSON without coupling to a renderer. Validate only permits scalar
// values and shallow lists in those fields.
type App struct {
	ID           string                 `json:"id"`
	Title        string                 `json:"title"`
	Prompt       string                 `json:"prompt,omitempty"`
	Lesson       LessonMetadata         `json:"lesson"`
	Modules      []Module               `json:"modules"`
	InitialState map[string]interface{} `json:"initial_state"`
	Limits       *InteractionLimits     `json:"limits,omitempty"`
}

type LessonMetadata struct {
	Subject   string   `json:"subject,omitempty"`
	Objective string   `json:"objective,omitempty"`
	Level     string   `json:"level,omitempty"`
	Tags      []string `json:"tags,omitempty"`
}

type InteractionLimits struct {
	MaxInteractions int `json:"max_interactions,omitempty"`
	MaxDurationMS   int `json:"max_duration_ms,omitempty"`
}

type Module struct {
	ID     string                 `json:"id"`
	Type   string                 `json:"type"`
	Params map[string]interface{} `json:"params"`
}

// Validate checks all structural, content, and allowlist constraints.
func (a App) Validate() error {
	if err := validateID("app id", a.ID); err != nil {
		return err
	}
	if err := validateText("app title", a.Title, MaxTitleLength, true); err != nil {
		return err
	}
	if err := validateText("prompt", a.Prompt, MaxTextLength, false); err != nil {
		return err
	}
	if err := validateLesson(a.Lesson); err != nil {
		return err
	}
	if len(a.Modules) == 0 || len(a.Modules) > MaxModules {
		return fmt.Errorf("modules must contain 1-%d modules", MaxModules)
	}
	if a.InitialState == nil {
		return errors.New("initial_state is required")
	}
	if err := validateState(a.InitialState); err != nil {
		return err
	}
	if err := validateLimits(a.Limits); err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(a.Modules))
	for i, module := range a.Modules {
		path := fmt.Sprintf("module %d", i+1)
		if err := validateID(path+" id", module.ID); err != nil {
			return err
		}
		if _, exists := seen[module.ID]; exists {
			return fmt.Errorf("duplicate module id %q", module.ID)
		}
		seen[module.ID] = struct{}{}
		if err := validateModule(path, module); err != nil {
			return err
		}
	}
	return nil
}

// Validate is the package-level form useful when the app is held by an
// interface or returned from another decoder.
func Validate(app App) error { return app.Validate() }

// Decode parses and validates one app document. Unknown JSON fields are
// rejected so the browser and server share one explicit contract.
func Decode(raw string) (App, error) {
	if len(raw) > MaxPayloadBytes {
		return App{}, ErrPayloadTooLarge
	}
	if strings.TrimSpace(raw) == "" {
		return App{}, errors.New("decode learning app: empty JSON document")
	}
	if err := validateJSONDepth([]byte(raw)); err != nil {
		return App{}, err
	}

	var app App
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&app); err != nil {
		return App{}, fmt.Errorf("decode learning app: %w", err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return App{}, errors.New("decode learning app: multiple JSON values are not allowed")
		}
		return App{}, fmt.Errorf("decode learning app trailing data: %w", err)
	}
	if err := app.Validate(); err != nil {
		return App{}, err
	}
	return app, nil
}

// Encode validates and serializes one app document.
func Encode(app App) (string, error) {
	if err := app.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(app)
	if err != nil {
		return "", fmt.Errorf("encode learning app: %w", err)
	}
	if len(b) > MaxPayloadBytes {
		return "", ErrPayloadTooLarge
	}
	return string(b), nil
}

func validateLesson(lesson LessonMetadata) error {
	for name, value := range map[string]string{
		"lesson.subject":   lesson.Subject,
		"lesson.objective": lesson.Objective,
		"lesson.level":     lesson.Level,
	} {
		if err := validateText(name, value, MaxTextLength, false); err != nil {
			return err
		}
	}
	if len(lesson.Tags) > MaxTags {
		return fmt.Errorf("lesson.tags must contain at most %d items", MaxTags)
	}
	for i, tag := range lesson.Tags {
		if err := validateText(fmt.Sprintf("lesson.tags[%d]", i), tag, 120, true); err != nil {
			return err
		}
	}
	return nil
}

func validateLimits(limits *InteractionLimits) error {
	if limits == nil {
		return nil
	}
	if limits.MaxInteractions < 1 || limits.MaxInteractions > MaxInteractions {
		return fmt.Errorf("limits.max_interactions must be between 1 and %d", MaxInteractions)
	}
	if limits.MaxDurationMS < MinDurationMS || limits.MaxDurationMS > MaxDurationMS {
		return fmt.Errorf("limits.max_duration_ms must be between %d and %d", MinDurationMS, MaxDurationMS)
	}
	return nil
}

func validateState(state map[string]interface{}) error {
	if len(state) > MaxStateFields {
		return fmt.Errorf("initial_state must contain at most %d fields", MaxStateFields)
	}
	for key, value := range state {
		if !stateKeyPattern.MatchString(key) {
			return fmt.Errorf("initial_state key %q is invalid", key)
		}
		if err := validateScalarOrList("initial_state."+key, value); err != nil {
			return err
		}
	}
	return nil
}

func validateModule(path string, module Module) error {
	if module.Type != TextType && module.Type != QuizType && module.Type != FlashcardsType && module.Type != ChoiceType && module.Type != SequenceType && module.Type != ProgressType {
		return fmt.Errorf("%w: %q", ErrUnknownModule, module.Type)
	}
	if module.Params == nil {
		return fmt.Errorf("%s params are required", path)
	}
	if len(module.Params) > MaxModuleParams {
		return fmt.Errorf("%s params must contain at most %d fields", path, MaxModuleParams)
	}
	allowed := moduleAllowedParams(module.Type)
	for key, value := range module.Params {
		if !stateKeyPattern.MatchString(key) {
			return fmt.Errorf("%s params key %q is invalid", path, key)
		}
		if !allowed[key] {
			return fmt.Errorf("%s has unknown parameter %q", path, key)
		}
		if err := validateScalarOrList(path+" params."+key, value); err != nil {
			return err
		}
	}

	switch module.Type {
	case TextType:
		return requireString(path, module.Params, "text", true)
	case QuizType:
		if err := requireString(path, module.Params, "question", true); err != nil {
			return err
		}
		options, err := requireStringList(path, module.Params, "options", 2, 6)
		if err != nil {
			return err
		}
		answer, err := requireInteger(path, module.Params, "answer")
		if err != nil {
			return err
		}
		if answer < 0 || answer >= len(options) {
			return fmt.Errorf("%s answer must refer to an option", path)
		}
		return optionalString(path, module.Params, "explanation")
	case FlashcardsType:
		fronts, err := requireStringList(path, module.Params, "fronts", 1, MaxListItems)
		if err != nil {
			return err
		}
		backs, err := requireStringList(path, module.Params, "backs", 1, MaxListItems)
		if err != nil {
			return err
		}
		if len(fronts) != len(backs) {
			return fmt.Errorf("%s fronts and backs must have the same length", path)
		}
		return optionalBool(path, module.Params, "shuffle")
	case ChoiceType:
		if err := requireString(path, module.Params, "prompt", true); err != nil {
			return err
		}
		if _, err := requireStringList(path, module.Params, "options", 2, 8); err != nil {
			return err
		}
		if _, ok := module.Params["answer"]; ok {
			answer, err := requireInteger(path, module.Params, "answer")
			if err != nil {
				return err
			}
			options, _ := requireStringList(path, module.Params, "options", 2, 8)
			if answer < 0 || answer >= len(options) {
				return fmt.Errorf("%s answer must refer to an option", path)
			}
		}
		return nil
	case SequenceType:
		if err := requireString(path, module.Params, "prompt", true); err != nil {
			return err
		}
		items, err := requireStringList(path, module.Params, "items", 2, 8)
		if err != nil {
			return err
		}
		order, err := requireIntegerList(path, module.Params, "order", len(items), len(items))
		if err != nil {
			return err
		}
		seen := make(map[int]struct{}, len(order))
		for _, value := range order {
			if value < 0 || value >= len(items) {
				return fmt.Errorf("%s order contains an invalid item index", path)
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("%s order contains a duplicate item index", path)
			}
			seen[value] = struct{}{}
		}
		return nil
	case ProgressType:
		if err := requireString(path, module.Params, "label", true); err != nil {
			return err
		}
		value, err := requireNumber(path, module.Params, "value")
		if err != nil {
			return err
		}
		maximum, err := requireNumber(path, module.Params, "max")
		if err != nil {
			return err
		}
		if maximum <= 0 || value < 0 || value > maximum {
			return fmt.Errorf("%s progress value must be between 0 and max", path)
		}
		return nil
	}
	return nil
}

func moduleAllowedParams(moduleType string) map[string]bool {
	switch moduleType {
	case TextType:
		return map[string]bool{"text": true}
	case QuizType:
		return map[string]bool{"question": true, "options": true, "answer": true, "explanation": true}
	case FlashcardsType:
		return map[string]bool{"fronts": true, "backs": true, "shuffle": true}
	case ChoiceType:
		return map[string]bool{"prompt": true, "options": true, "answer": true}
	case SequenceType:
		return map[string]bool{"prompt": true, "items": true, "order": true}
	case ProgressType:
		return map[string]bool{"label": true, "value": true, "max": true}
	default:
		return nil
	}
}

func requireString(path string, params map[string]interface{}, key string, nonBlank bool) error {
	value, ok := params[key]
	if !ok {
		return fmt.Errorf("%s params.%s is required", path, key)
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s params.%s must be a string", path, key)
	}
	return validateText(path+" params."+key, text, MaxTextLength, nonBlank)
}

func optionalString(path string, params map[string]interface{}, key string) error {
	if _, ok := params[key]; !ok {
		return nil
	}
	return requireString(path, params, key, false)
}

func optionalBool(path string, params map[string]interface{}, key string) error {
	if _, ok := params[key]; !ok {
		return nil
	}
	if _, ok := params[key].(bool); !ok {
		return fmt.Errorf("%s params.%s must be a boolean", path, key)
	}
	return nil
}

func requireStringList(path string, params map[string]interface{}, key string, min, max int) ([]string, error) {
	value, ok := params[key]
	if !ok {
		return nil, fmt.Errorf("%s params.%s is required", path, key)
	}
	values, ok := listValues(value)
	if !ok || len(values) < min || len(values) > max {
		return nil, fmt.Errorf("%s params.%s must contain %d-%d strings", path, key, min, max)
	}
	result := make([]string, len(values))
	for i, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s params.%s[%d] must be a string", path, key, i)
		}
		if err := validateText(fmt.Sprintf("%s params.%s[%d]", path, key, i), text, MaxTextLength, true); err != nil {
			return nil, err
		}
		result[i] = text
	}
	return result, nil
}

func requireInteger(path string, params map[string]interface{}, key string) (int, error) {
	value, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("%s params.%s is required", path, key)
	}
	number, ok := numberValue(value)
	if !ok || math.Trunc(number) != number || number < math.MinInt || number > math.MaxInt {
		return 0, fmt.Errorf("%s params.%s must be an integer", path, key)
	}
	return int(number), nil
}

func requireIntegerList(path string, params map[string]interface{}, key string, min, max int) ([]int, error) {
	value, ok := params[key]
	if !ok {
		return nil, fmt.Errorf("%s params.%s is required", path, key)
	}
	values, ok := listValues(value)
	if !ok || len(values) < min || len(values) > max {
		return nil, fmt.Errorf("%s params.%s must contain %d-%d integers", path, key, min, max)
	}
	result := make([]int, len(values))
	for i, value := range values {
		number, ok := numberValue(value)
		if !ok || math.Trunc(number) != number || number < math.MinInt || number > math.MaxInt {
			return nil, fmt.Errorf("%s params.%s[%d] must be an integer", path, key, i)
		}
		result[i] = int(number)
	}
	return result, nil
}

func requireNumber(path string, params map[string]interface{}, key string) (float64, error) {
	value, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("%s params.%s is required", path, key)
	}
	number, ok := numberValue(value)
	if !ok {
		return 0, fmt.Errorf("%s params.%s must be a finite number", path, key)
	}
	return number, nil
}

func validateScalarOrList(path string, value interface{}) error {
	if isScalar(value) {
		if text, ok := value.(string); ok {
			return validateText(path, text, MaxTextLength, false)
		}
		if number, ok := numberValue(value); ok && !isFiniteNumber(number) {
			return fmt.Errorf("%s must be a finite number", path)
		}
		if _, ok := numberValue(value); !ok {
			switch value.(type) {
			case bool:
			default:
				return fmt.Errorf("%s must be a scalar or a list of scalars", path)
			}
		}
		return nil
	}
	values, ok := listValues(value)
	if !ok {
		return fmt.Errorf("%s must be a scalar or a list of scalars", path)
	}
	if len(values) > MaxListItems {
		return fmt.Errorf("%s may contain at most %d items", path, MaxListItems)
	}
	for i, item := range values {
		if !isScalar(item) {
			return fmt.Errorf("%s[%d] must be a scalar", path, i)
		}
		if err := validateScalarOrList(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
			return err
		}
	}
	return nil
}

func isScalar(value interface{}) bool {
	if value == nil {
		return false
	}
	if _, ok := value.(string); ok {
		return true
	}
	if _, ok := value.(bool); ok {
		return true
	}
	_, ok := numberValue(value)
	return ok
}

func listValues(value interface{}) ([]interface{}, bool) {
	if value == nil {
		return nil, false
	}
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return nil, false
	}
	values := make([]interface{}, v.Len())
	for i := range values {
		values[i] = v.Index(i).Interface()
	}
	return values, true
}

func numberValue(value interface{}) (float64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(number.String(), 64)
		return parsed, err == nil && isFiniteNumber(parsed)
	case float64:
		return number, isFiniteNumber(number)
	case float32:
		parsed := float64(number)
		return parsed, isFiniteNumber(parsed)
	case int:
		return float64(number), true
	case int8:
		return float64(number), true
	case int16:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		parsed := float64(number)
		return parsed, isFiniteNumber(parsed)
	case uint:
		return float64(number), true
	case uint8:
		return float64(number), true
	case uint16:
		return float64(number), true
	case uint32:
		return float64(number), true
	case uint64:
		parsed := float64(number)
		return parsed, isFiniteNumber(parsed)
	default:
		return 0, false
	}
}

func isFiniteNumber(number float64) bool { return !math.IsNaN(number) && !math.IsInf(number, 0) }

func validateID(name, value string) error {
	if len(value) > MaxIDLength || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s must match [a-z][a-z0-9_-]{0,63}", name)
	}
	return nil
}

func validateText(name, value string, max int, nonBlank bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if nonBlank && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be blank", name)
	}
	if utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s exceeds %d characters", name, max)
	}
	if containsUnsafeContent(value) {
		return fmt.Errorf("%w: %s contains markup, script, or external URL content", ErrUnsafeContent, name)
	}
	return nil
}

func containsUnsafeContent(value string) bool {
	lower := strings.ToLower(value)
	for _, fragment := range []string{"<", ">", "javascript:", "vbscript:", "data:", "http://", "https://", "ftp://", "//", "onerror=", "onclick=", "onload="} {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func validateJSONDepth(raw []byte) error {
	depth := 0
	inString := false
	escaped := false
	for _, char := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			continue
		}
		switch char {
		case '{', '[':
			depth++
			if depth > MaxJSONDepth {
				return ErrJSONTooDeep
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return errors.New("decode learning app: unbalanced JSON delimiters")
			}
		}
	}
	return nil
}
