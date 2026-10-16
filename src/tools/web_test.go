package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebToolFetchesHTMLAsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Hi</title>
			<style>body{color:red}</style>
			<script>alert("nope")</script></head>
			<body><h1>Hello &amp; welcome</h1>
			<p>First   paragraph.</p>
			<ul><li>one</li><li>two</li></ul></body></html>`))
	}))
	defer srv.Close()

	out := NewWebTool().Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if out.Code != 0 {
		t.Fatalf("unexpected error: %q code=%d", out.Error, out.Code)
	}
	for _, want := range []string{"Hello & welcome", "First paragraph.", "one", "two"} {
		if !strings.Contains(out.Output, want) {
			t.Errorf("output missing %q:\n%s", want, out.Output)
		}
	}
	for _, bad := range []string{"<h1>", "alert(", "color:red"} {
		if strings.Contains(out.Output, bad) {
			t.Errorf("output should not contain %q:\n%s", bad, out.Output)
		}
	}
}

func TestWebToolPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("just text"))
	}))
	defer srv.Close()

	out := NewWebTool().Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if out.Code != 0 || !strings.Contains(out.Output, "just text") {
		t.Fatalf("got %+v", out)
	}
}

func TestWebToolRejectsBadArgs(t *testing.T) {
	cases := []string{`not json`, `{}`, `{"url":"ftp://example.com"}`, `{"url":"/relative"}`}
	for _, args := range cases {
		out := NewWebTool().Execute(context.Background(), args)
		if out.Code == 0 {
			t.Errorf("args %q: expected failure, got %+v", args, out)
		}
	}
}

func TestWebToolHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	out := NewWebTool().Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if out.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", out.Code)
	}
	if !strings.Contains(out.Error, "404") {
		t.Fatalf("error = %q, want mentions 404", out.Error)
	}
}

func TestWebToolTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("x", 500)))
	}))
	defer srv.Close()

	wt := NewWebTool()
	wt.MaxChars = 100
	out := wt.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if !strings.Contains(out.Output, "truncated") {
		t.Fatalf("expected truncation marker:\n%s", out.Output)
	}
}

func TestWebToolNameAndSchema(t *testing.T) {
	wt := NewWebTool()
	if wt.Name() != "web" {
		t.Fatalf("Name = %q", wt.Name())
	}
	if !strings.Contains(string(wt.Parameters()), "url") {
		t.Fatalf("schema missing url: %s", wt.Parameters())
	}
}
