package profiles

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func example(t *testing.T) map[string]any {
	t.Helper()
	src, err := os.ReadFile("../../profiles/example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := FromYAML(src)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(doc, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExampleProfileIsValid(t *testing.T) {
	doc, _ := json.Marshal(example(t))
	if v, err := Validate(doc); err != nil || v != 2 {
		t.Fatalf("version %d, err %v", v, err)
	}
}

// asV1 turns the (v2) example into a v1 document: no v2 fields, no v2 hard constraints.
func asV1(m map[string]any) map[string]any {
	m["schema_version"] = 1
	delete(m, "target_roles")
	delete(m, "languages")
	prefs := m["preferences"].(map[string]any)
	delete(prefs, "remote_regions")
	delete(prefs, "relocation")
	prefs["hard_constraints"] = []any{"remote_only", "salary"}
	return m
}

func TestVersion1StillValid(t *testing.T) {
	doc, _ := json.Marshal(asV1(example(t)))
	if v, err := Validate(doc); err != nil || v != 1 {
		t.Fatalf("version %d, err %v", v, err)
	}
}

func TestInvalidProfiles(t *testing.T) {
	for name, tc := range map[string]struct {
		edit    func(m map[string]any)
		wantLoc string
	}{
		"unknown schema version": {func(m map[string]any) { m["schema_version"] = 3 }, "/schema_version"},
		"string schema version":  {func(m map[string]any) { m["schema_version"] = "2" }, "/schema_version"},
		"v2 field in a v1 document": {func(m map[string]any) {
			asV1(m)["target_roles"] = []any{"Data Engineer"}
		}, "/"},
		"v2 hard constraint in v1": {func(m map[string]any) {
			asV1(m)["preferences"].(map[string]any)["hard_constraints"] = []any{"languages"}
		}, "/preferences/hard_constraints/0"},
		"language name, not code": {func(m map[string]any) {
			m["languages"].([]any)[0].(map[string]any)["language"] = "German"
		}, "/languages/0/language"},
		"level not CEFR": {func(m map[string]any) {
			m["languages"].([]any)[0].(map[string]any)["level"] = "fluent"
		}, "/languages/0/level"},
		"relocation without willing": {func(m map[string]any) {
			delete(m["preferences"].(map[string]any)["relocation"].(map[string]any), "willing")
		}, "/preferences/relocation"},
		"too many target roles": {func(m map[string]any) {
			roles := []any{}
			for i := range 11 {
				roles = append(roles, strings.Repeat("r", i+1))
			}
			m["target_roles"] = roles
		}, "/target_roles"},
		"missing seniority": {func(m map[string]any) { delete(m, "seniority") }, "/"},
		"unknown field":     {func(m map[string]any) { m["ssn"] = "123" }, "/"},
		"bad month": {func(m map[string]any) {
			m["experience"].([]any)[0].(map[string]any)["start"] = "March 2021"
		}, "/experience/0/start"},
		"lowercase currency": {func(m map[string]any) {
			m["preferences"].(map[string]any)["salary"].(map[string]any)["currency"] = "eur"
		}, "/preferences/salary/currency"},
		"unknown hard constraint": {func(m map[string]any) {
			m["preferences"].(map[string]any)["hard_constraints"] = []any{"vibes"}
		}, "/preferences/hard_constraints/0"},
		"huge text": {func(m map[string]any) { m["headline"] = strings.Repeat("x", 2001) }, "/headline"},
	} {
		t.Run(name, func(t *testing.T) {
			m := example(t)
			tc.edit(m)
			doc, _ := json.Marshal(m)
			_, err := Validate(doc)
			var inv *InvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("want InvalidError, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantLoc+":") {
				t.Errorf("no problem at %s: %v", tc.wantLoc, err)
			}
			t.Log(err)
		})
	}
}

func TestValidateRejectsNonJSONAndHugeDocuments(t *testing.T) {
	for _, doc := range [][]byte{[]byte("not json"), make([]byte, MaxDocumentBytes+1)} {
		var inv *InvalidError
		if _, err := Validate(doc); !errors.As(err, &inv) {
			t.Errorf("got %v", err)
		}
	}
}
