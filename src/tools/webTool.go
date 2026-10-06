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
	"stubbs/src/types"
	"time"
)

const (
	defaultWebTimeout   = 30 * time.Second
	defaultWebMaxOutput = 32 << 10
	defaultWebUserAgent = "stubbs/dev"
)

// WebFetchTool fetches a URL over HTTP(S) and returns its contents as text.
// HTML responses are reduced to readable text; other content types are
// returned verbatim (truncated to MaxOutput).
type WebFetchTool struct {
	Timeout   time.Duration
	MaxOutput int
	UserAgent string
	Client    *http.Client
}

func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{
		Timeout:   defaultWebTimeout,
		MaxOutput: defaultWebMaxOutput,
		UserAgent: defaultWebUserAgent,
	}
}

func (t *WebFetchTool) Name() string { return "webfetch" }

func (t *WebFetchTool) Description() string {
	return "Fetch a web page over HTTP(S) and return its contents as text. " +
		"HTML is converted to plain text; the result is truncated if very large."
}

func (t *WebFetchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"url": {
			"type": "string",
			"description": "absolute http(s) URL to fetch"
		}
	},
	"required": ["url"]
}`)
}

type webFetchArgs struct {
	URL string `json:"url"`
}

func (t *WebFetchTool) Execute(ctx context.Context, args string) types.ExecutionOutput {
	var a webFetchArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("webfetch: invalid arguments: %v", err), Code: -1}
	}
	raw := strings.TrimSpace(a.URL)
	u, err := url.Parse(raw)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("webfetch: invalid URL: %v", err), Code: -1}
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return types.ExecutionOutput{Error: fmt.Sprintf("webfetch: unsupported scheme %q (want http or https)", u.Scheme), Code: -1}
	}
	if u.Host == "" {
		return types.ExecutionOutput{Error: "webfetch: URL has no host", Code: -1}
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultWebTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("webfetch: build request: %v", err), Code: -1}
	}
	ua := t.UserAgent
	if ua == "" {
		ua = defaultWebUserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.8")

	resp, err := t.client().Do(req)
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("webfetch: %v", err), Code: -1}
	}
	defer resp.Body.Close()

	max := t.maxOutput()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(max)+1))
	if err != nil {
		return types.ExecutionOutput{Error: fmt.Sprintf("webfetch: read body: %v", err), Code: -1}
	}
	truncated := len(body) > max
	if truncated {
		body = body[:max]
	}

	text := string(body)
	if isHTML(resp.Header.Get("Content-Type")) {
		text = htmlToText(text)
	} else {
		text = strings.TrimSpace(text)
	}
	if truncated {
		text += fmt.Sprintf("\n... [output truncated at %d bytes]", max)
	}

	out := types.ExecutionOutput{Output: text}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Error = fmt.Sprintf("webfetch: HTTP %s", resp.Status)
		out.Code = -1
	}
	return out
}

func (t *WebFetchTool) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

func (t *WebFetchTool) maxOutput() int {
	if t.MaxOutput > 0 {
		return t.MaxOutput
	}
	return defaultWebMaxOutput
}

func isHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

var (
	reDropBlocks = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script[^>]*>.*?</script\s*>`),
		regexp.MustCompile(`(?is)<style[^>]*>.*?</style\s*>`),
		regexp.MustCompile(`(?is)<noscript[^>]*>.*?</noscript\s*>`),
		regexp.MustCompile(`(?is)<template[^>]*>.*?</template\s*>`),
		regexp.MustCompile(`(?is)<!--.*?-->`),
	}
	reBlockBreak  = regexp.MustCompile(`(?i)</(p|div|li|ul|ol|tr|table|section|article|header|footer|pre|blockquote|h[1-6])>|<br\s*/?>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]*>`)
	reInlineSpace = regexp.MustCompile(`[ \t\f\v]+`)
	reLineSpace   = regexp.MustCompile(` *\n *`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText strips markup so the model sees readable text: script/style
// contents are dropped, block boundaries become newlines, and whitespace is
// normalized.
func htmlToText(s string) string {
	for _, re := range reDropBlocks {
		s = re.ReplaceAllString(s, " ")
	}
	s = reBlockBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = reInlineSpace.ReplaceAllString(s, " ")
	s = reLineSpace.ReplaceAllString(s, "\n")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
