package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"stubbs/src/types"
)

const (
	defaultWebTimeout  = 30 * time.Second
	defaultWebMaxChars = 20_000
	// maxWebBodyBytes caps how much of a response is read into memory so a
	// hostile or accidental huge page cannot exhaust the process.
	maxWebBodyBytes = 5 << 20
)

// WebTool fetches a URL over HTTP(S) and returns its readable text content.
// HTML documents are reduced to plain text (scripts/styles stripped, tags
// removed, entities decoded) so the model gets useful prose instead of markup.
type WebTool struct {
	// Client is the HTTP client used for requests. Nil uses a client with
	// Timeout.
	Client *http.Client
	// Timeout bounds a single fetch. Zero uses defaultWebTimeout.
	Timeout time.Duration
	// MaxChars caps the returned text. Zero uses defaultWebMaxChars.
	MaxChars int
	// UserAgent overrides the request User-Agent.
	UserAgent string
}

func NewWebTool() *WebTool {
	return &WebTool{
		Client:    &http.Client{Timeout: defaultWebTimeout},
		Timeout:   defaultWebTimeout,
		MaxChars:  defaultWebMaxChars,
		UserAgent: "stubbs-chat/0.1",
	}
}

func (w *WebTool) Name() string { return "web" }

func (w *WebTool) Description() string {
	return "Browse the web: fetch a URL over HTTP(S) and return its readable " +
		"text content. HTML pages are converted to plain text. Use this to look " +
		"up current information, read documentation, or research a topic."
}

func (w *WebTool) Parameters() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The absolute http(s) URL to fetch",
			},
		},
		"required": []string{"url"},
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return []byte(`{"type":"object"}`)
	}
	return encoded
}

type webArgs struct {
	URL string `json:"url"`
}

func (w *WebTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var in webArgs
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("web: bad arguments: %v", err), Code: -1}
	}
	raw := strings.TrimSpace(in.URL)
	if raw == "" {
		return types.ExecutionOutput{Error: "web: url cannot be empty", Code: -1}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("web: invalid url: %v", err), Code: -1}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return types.ExecutionOutput{
			Error: fmt.Sprintf("web: unsupported scheme %q (only http and https)", u.Scheme),
			Code:  -1,
		}
	}
	if u.Host == "" {
		return types.ExecutionOutput{Error: "web: url is missing a host", Code: -1}
	}

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = defaultWebTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(runCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("web: %v", err), Code: -1}
	}
	if ua := w.userAgent(); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.8")

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("web: request failed: %v", err), Code: -1}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBodyBytes))
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("web: read body: %v", err), Code: -1}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return types.ExecutionOutput{
			Error: fmt.Sprintf("web: HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)),
			Code:  resp.StatusCode,
		}
	}

	text := string(body)
	if isHTML(resp.Header.Get("Content-Type")) {
		text = htmlToText(text)
	} else {
		text = strings.TrimSpace(text)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "URL: %s\nStatus: %d\n\n", u.String(), resp.StatusCode)
	b.WriteString(text)

	out := b.String()
	if max := w.maxChars(); max > 0 && len(out) > max {
		out = out[:max] + fmt.Sprintf("\n\n... [content truncated at %d characters]", max)
	}
	return types.ExecutionOutput{Output: out, Code: 0}
}

func (w *WebTool) maxChars() int {
	if w.MaxChars > 0 {
		return w.MaxChars
	}
	return defaultWebMaxChars
}

func (w *WebTool) userAgent() string {
	if w.UserAgent != "" {
		return w.UserAgent
	}
	return "stubbs-chat/0.1"
}

// isHTML reports whether a Content-Type denotes an HTML document. An empty
// Content-Type is treated as HTML, since that is the common case for pages.
func isHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	if ct == "" {
		return true
	}
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

var (
	webScriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript|template|svg)\b[^>]*>.*?</\s*(script|style|noscript|template|svg)\s*>`)
	webBlockTagRe    = regexp.MustCompile(`(?i)<\s*/?\s*(br|p|div|li|ul|ol|tr|td|th|h[1-6]|section|article|header|footer|nav|aside|blockquote|pre|table|hr)\b[^>]*>`)
	webTagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
	webSpaceRe       = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
	webBlankLineRe   = regexp.MustCompile(`\n{3,}`)
)

// htmlToText strips markup from an HTML document and returns collapsed,
// readable plain text. It is deliberately simple: a full parser would be
// better, but this keeps the chat binary dependency-free.
func htmlToText(s string) string {
	s = webScriptStyleRe.ReplaceAllString(s, " ")
	s = webBlockTagRe.ReplaceAllString(s, "\n")
	s = webTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = webSpaceRe.ReplaceAllString(s, " ")

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = webBlankLineRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
