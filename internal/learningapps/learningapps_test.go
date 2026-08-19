package learningapps

import (
	"strings"
	"testing"
)

func validApp() App {
	return App{
		ID:     "fotossintese-1",
		Title:  "Fase clara",
		Prompt: "Revise os conceitos principais.",
		Lesson: LessonMetadata{Subject: "Biologia", Objective: "Reconhecer o fluxo de energia", Level: "básico", Tags: []string{"energia"}},
		Modules: []Module{
			{ID: "intro", Type: TextType, Params: map[string]interface{}{"text": "A luz excita elétrons."}},
			{ID: "check", Type: QuizType, Params: map[string]interface{}{"question": "Qual molécula armazena energia?", "options": []interface{}{"ATP", "DNA"}, "answer": 0, "explanation": "ATP funciona como uma moeda energética."}},
			{ID: "progress", Type: ProgressType, Params: map[string]interface{}{"label": "Progresso", "value": 1, "max": 3}},
		},
		InitialState: map[string]interface{}{"attempts": 0},
		Limits:       &InteractionLimits{MaxInteractions: 20, MaxDurationMS: 60_000},
	}
}

func TestAppValidateAndRoundTrip(t *testing.T) {
	app := validApp()
	encoded, err := Encode(app)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, err := Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != encodedAgain {
		t.Fatalf("round trip changed JSON:\n%s\n%s", encoded, encodedAgain)
	}
}

func TestDecodeRejectsUnsafeOrUnknownContent(t *testing.T) {
	tests := []string{
		`{"id":"app","title":"x","lesson":{},"modules":[{"id":"m","type":"canvas","params":{}}],"initial_state":{}}`,
		`{"id":"app","title":"<script>","lesson":{},"modules":[{"id":"m","type":"text","params":{"text":"x"}}],"initial_state":{}}`,
		`{"id":"app","title":"x","lesson":{},"modules":[{"id":"m","type":"text","params":{"text":"https://evil.test"}}],"initial_state":{}}`,
		`{"id":"app","title":"x","lesson":{},"modules":[{"id":"m","type":"text","params":{"text":"x"}}],"initial_state":{},"extra":true}`,
	}
	for _, raw := range tests {
		if _, err := Decode(raw); err == nil {
			t.Errorf("Decode(%s) succeeded, want rejection", raw)
		}
	}
}

func TestValidateRejectsInvalidModuleDataAndLimits(t *testing.T) {
	app := validApp()
	app.Modules[1].Params["answer"] = 9
	if err := app.Validate(); err == nil {
		t.Fatal("out-of-range quiz answer should be rejected")
	}
	app = validApp()
	app.Modules[0].Params["text"] = strings.Repeat("x", MaxTextLength+1)
	if err := app.Validate(); err == nil {
		t.Fatal("oversized text should be rejected")
	}
	app = validApp()
	app.Limits = &InteractionLimits{MaxInteractions: 0, MaxDurationMS: 500}
	if err := app.Validate(); err == nil {
		t.Fatal("invalid limits should be rejected")
	}
	app = validApp()
	app.Modules = append(app.Modules, Module{ID: "bad", Type: SequenceType, Params: map[string]interface{}{"prompt": "Ordene", "items": []interface{}{"a", "b"}, "order": []interface{}{0, 0}}})
	if err := app.Validate(); err == nil {
		t.Fatal("non-permutation sequence should be rejected")
	}
}

func TestDecodeRejectsDeepJSONAndOversizedPayload(t *testing.T) {
	deep := `{"id":"app","title":"x","lesson":{},"modules":[{"id":"m","type":"text","params":{"text":"x"}}],"initial_state":{"a":[[[[[[[[1]]]]]]]]}}`
	if _, err := Decode(deep); err == nil {
		t.Fatal("deep JSON should be rejected")
	}
	if _, err := Decode(strings.Repeat("x", MaxPayloadBytes+1)); err != ErrPayloadTooLarge {
		t.Fatalf("oversized payload error = %v, want ErrPayloadTooLarge", err)
	}
}
