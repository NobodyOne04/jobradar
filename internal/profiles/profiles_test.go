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
	if err := Validate(doc); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidProfiles(t *testing.T) {
	for name, tc := range map[string]struct {
		edit    func(m map[string]any)
		wantLoc string
	}{
		"wrong schema version": {func(m map[string]any) { m["schema_version"] = 2 }, "/schema_version"},
		"missing seniority":    {func(m map[string]any) { delete(m, "seniority") }, "/"},
		"unknown field":        {func(m map[string]any) { m["ssn"] = "123" }, "/"},
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
			err := Validate(doc)
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
		if err := Validate(doc); !errors.As(err, &inv) {
			t.Errorf("got %v", err)
		}
	}
}
