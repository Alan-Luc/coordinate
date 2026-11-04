package savedpi

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestValidateRejectsInvalidMessageFields(t *testing.T) {
	for _, scenario := range []struct{ name, message string }{
		{"null content", `{"role":"user","content":null,"timestamp":1}`},
		{"quoted timestamp", `{"role":"user","content":"hello","timestamp":"1"}`},
		{"null text block", `{"role":"user","content":[{"type":"text","text":null}],"timestamp":1}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			path := fixture(t)
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			header, _, ok := bytes.Cut(body, []byte("\n"))
			if !ok {
				t.Fatal("fixture header missing")
			}
			entry := `{"type":"message","id":"entry1","parentId":null,"timestamp":"2026-10-29T00:00:00Z","message":` + scenario.message + "}\n"
			candidate := append(append(header, '\n'), entry...)
			if err := os.WriteFile(path, candidate, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := Validate(path); err == nil {
				t.Fatal("invalid native message accepted")
			}
		})
	}
}

func TestValidateAcceptsNullContextReplacement(t *testing.T) {
	path := fixture(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte(`{"type":"context_edit","id":"entry2","parentId":"entry1","timestamp":"2026-10-29T00:00:01Z","targetId":"entry1","replacement":null}`+"\n")...)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(path); err != nil {
		t.Fatalf("native context omission rejected: %v", err)
	}
}

func TestValidateRejectsQuotedUsageNumber(t *testing.T) {
	path := fixture(t)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := `{"type":"usage","id":"entry2","parentId":"entry1","timestamp":"2026-10-29T00:00:01Z","kind":"cache_warm","usage":{"input":"1","output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":1,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`
	body = append(body, []byte(strings.TrimSpace(entry)+"\n")...)
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(path); err == nil {
		t.Fatal("quoted usage number accepted")
	}
}
