package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchHTMLToText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>T</title>
			<style>.x{color:red}</style>
			<script>alert("nope")</script></head>
			<body><h1>Hello</h1><p>World &amp; friends</p><div>bye</div></body></html>`)
	}))
	defer srv.Close()

	out := NewWebFetchTool().Execute(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if out.Error != "" || out.Code != 0 {
		t.Fatalf("unexpected output: %+v", out)
	}
	for _, want := range []string{"Hello", "World & friends", "bye"} {
		if !strings.Contains(out.Output, want) {
			t.Errorf("output missing %q: %q", want, out.Output)
		}
	}
	for _, unwanted := range []string{"<", "alert", "color:red"} {
		if strings.Contains(out.Output, unwanted) {
			t.Errorf("output should not contain %q: %q", unwanted, out.Output)
		}
	}
}

func TestWebFetchNonHTMLPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	out := NewWebFetchTool().Execute(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if out.Error != "" || out.Code != 0 {
		t.Fatalf("unexpected output: %+v", out)
	}
	if out.Output != `{"ok":true}` {
		t.Fatalf("output = %q", out.Output)
	}
}

func TestWebFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()

	out := NewWebFetchTool().Execute(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if out.Code != -1 || !strings.Contains(out.Error, "404") {
		t.Fatalf("want 404 error, got %+v", out)
	}
}

func TestWebFetchRejectsBadScheme(t *testing.T) {
	for _, raw := range []string{`{"url":"ftp://example.com"}`, `{"url":"not a url"}`, `{"url":""}`} {
		out := NewWebFetchTool().Execute(context.Background(), raw)
		if out.Code != -1 || out.Error == "" {
			t.Fatalf("%s: want error, got %+v", raw, out)
		}
	}
}

func TestWebFetchTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("a", 1000))
	}))
	defer srv.Close()

	tool := NewWebFetchTool()
	tool.MaxOutput = 10
	out := tool.Execute(context.Background(), fmt.Sprintf(`{"url":%q}`, srv.URL))
	if out.Code != 0 {
		t.Fatalf("unexpected output: %+v", out)
	}
	if !strings.HasPrefix(out.Output, "aaaaaaaaaa") || !strings.Contains(out.Output, "truncated") {
		t.Fatalf("output not truncated: %q", out.Output)
	}
}
