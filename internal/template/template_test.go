package template

import (
	"strings"
	"testing"
)

func TestRenderAndMissingVariables(t *testing.T) {
	got, err := Render("Hi {{ name }} at {{company}}", map[string]string{"name": "Sam", "company": "Acme"})
	if err != nil || got != "Hi Sam at Acme" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err = Render("{{one}} {{two}}", map[string]string{"one": "1"}); err == nil || !strings.Contains(err.Error(), "two") {
		t.Fatalf("expected missing variable error, got %v", err)
	}
}
func TestParseCSVAndDedupe(t *testing.T) {
	rows, err := ParseCSV(strings.NewReader("email,name,company\na@example.com,Ann,Acme\nA@example.com,Dup,Other\nb@example.com,Bob,Co\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Values["name"] != "Ann" || rows[1].Email != "b@example.com" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}
func TestParseCSVErrors(t *testing.T) {
	cases := []string{"name\nSam\n", "email,name\na@example.com,Sam,extra\n", "email\na@example.com\n\"broken"}
	for _, input := range cases {
		if _, err := ParseCSV(strings.NewReader(input)); err == nil {
			t.Fatalf("expected malformed CSV error for %q", input)
		}
	}
}
