package main

import (
	"encoding/base64"
	"testing"
)

func TestParseJMBase64HTML(t *testing.T) {
	want := `<html><div id="book-name">title</div></html>`
	encoded := base64.StdEncoding.EncodeToString([]byte(want))
	got := parseJMBase64HTML(`const html = base64DecodeUtf8("` + encoded + `")`)
	if got != want {
		t.Fatalf("parseJMBase64HTML() = %q; want %q", got, want)
	}
}

func TestParseJMBase64HTMLPlain(t *testing.T) {
	plain := "<html>plain</html>"
	if got := parseJMBase64HTML(plain); got != plain {
		t.Fatalf("parseJMBase64HTML() = %q; want %q", got, plain)
	}
}
