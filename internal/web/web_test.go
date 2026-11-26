package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTMLToText(t *testing.T) {
	doc := `<!doctype html><html><head><title>Test Page</title><style>.x{}</style></head>
<body><h1>Heading</h1><p>Hello <b>world</b>.</p><script>alert(1)</script>
<ul><li>One</li><li>Two</li></ul><pre>a
 b</pre><a href="https://example.com">Link</a></body></html>`
	title, text, err := HTMLToText([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if title != "Test Page" {
		t.Fatalf("title = %q", title)
	}
	for _, want := range []string{"# Heading", "Hello world", "- One", "- Two", "a\nb", "Link (https://example.com)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "alert") || strings.Contains(text, ".x{}") {
		t.Fatalf("script/style leaked:\n%s", text)
	}
}

func TestFetchHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Hi</title></head><body><p>Body text</p></body></html>`)
	}))
	defer server.Close()
	res, err := Fetch(t.Context(), server.URL, FetchOptions{AllowPrivate: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || res.Title != "Hi" || !strings.Contains(res.Content, "Body text") {
		t.Fatalf("res = %#v", res)
	}
	if res.FinalURL != server.URL+"/" && res.FinalURL != server.URL {
		t.Fatalf("final url = %q", res.FinalURL)
	}
}

func TestFetchBlocksPrivateByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secret")
	}))
	defer server.Close()
	if _, err := Fetch(t.Context(), server.URL, FetchOptions{}, 0); err == nil {
		t.Fatal("expected private address to be blocked")
	}
}

func TestFetchAllowedHosts(t *testing.T) {
	if err := checkAllowedHost("api.example.com", []string{"*.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := checkAllowedHost("example.com", []string{"*.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := checkAllowedHost("evil.com", []string{"*.example.com"}); err == nil {
		t.Fatal("expected host rejection")
	}
}

func TestFetchRedirectBlockedByHosts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/", http.StatusFound)
	}))
	defer server.Close()
	_, err := Fetch(t.Context(), server.URL, FetchOptions{AllowPrivate: true, AllowedHosts: []string{"127.0.0.1"}}, 0)
	if err == nil || !strings.Contains(err.Error(), "allowed_hosts") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchTruncates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 100))
	}))
	defer server.Close()
	res, err := Fetch(t.Context(), server.URL, FetchOptions{AllowPrivate: true, MaxBytes: 10}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Content) != 10 {
		t.Fatalf("res = %#v", res)
	}
}

func TestFetchContentTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("y", 100))
	}))
	defer server.Close()
	res, err := Fetch(t.Context(), server.URL, FetchOptions{AllowPrivate: true}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || !strings.HasPrefix(res.Content, strings.Repeat("y", 10)) {
		t.Fatalf("res = %#v", res)
	}
}

func TestFetch404ReturnsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, "missing")
	}))
	defer server.Close()
	res, err := Fetch(t.Context(), server.URL, FetchOptions{AllowPrivate: true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 404 || res.Content != "missing" {
		t.Fatalf("res = %#v", res)
	}
}

func TestSearchTavily(t *testing.T) {
	t.Setenv("TEST_TAVILY_KEY", "secret")
	var auth string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"A","url":"https://a.example","content":"snippet"}]}`)
	}))
	defer server.Close()
	res, err := Search(t.Context(), "golang", SearchOptions{
		Provider: "tavily", BaseURL: server.URL, APIKeyEnv: "TEST_TAVILY_KEY", MaxResults: 3, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer secret" || body["query"] != "golang" {
		t.Fatalf("auth=%q body=%v", auth, body)
	}
	if len(res.Results) != 1 || res.Results[0].Title != "A" || res.Results[0].Snippet != "snippet" {
		t.Fatalf("res = %#v", res)
	}
}

func TestSearchSearxng(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") != "json" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"B","url":"https://b.example","content":"s2"}]}`)
	}))
	defer server.Close()
	res, err := Search(t.Context(), "query", SearchOptions{Provider: "searxng", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || res.Results[0].URL != "https://b.example" {
		t.Fatalf("res = %#v", res)
	}
}

func TestSearchMissingKey(t *testing.T) {
	t.Setenv("MISSING_KEY_FOR_TEST", "")
	_, err := Search(t.Context(), "query", SearchOptions{Provider: "tavily", APIKeyEnv: "MISSING_KEY_FOR_TEST"})
	if err == nil || !strings.Contains(err.Error(), "MISSING_KEY_FOR_TEST") {
		t.Fatalf("err = %v", err)
	}
}
