package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// SearchOptions configures a web search.
type SearchOptions struct {
	Provider   string
	BaseURL    string
	APIKeyEnv  string
	MaxResults int
	Timeout    time.Duration
}

// Hit is one search result.
type Hit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchResult is the result of a web search.
type SearchResult struct {
	Query    string `json:"query"`
	Provider string `json:"provider"`
	Results  []Hit  `json:"results"`
}

const maxSearchBytes = 1024 * 1024

// Search queries the configured search provider.
func Search(ctx context.Context, query string, opts SearchOptions) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, fmt.Errorf("query is required")
	}
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	if provider == "" {
		provider = "tavily"
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 5
	}
	if opts.MaxResults > 20 {
		opts.MaxResults = 20
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 20 * time.Second
	}
	if opts.Timeout > 2*time.Minute {
		opts.Timeout = 2 * time.Minute
	}
	var key string
	if opts.APIKeyEnv != "" {
		key = os.Getenv(opts.APIKeyEnv)
	}
	client := &http.Client{Timeout: opts.Timeout}
	runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	switch provider {
	case "tavily":
		if key == "" {
			return SearchResult{}, fmt.Errorf("environment variable %s is empty (required for tavily search)", opts.APIKeyEnv)
		}
		return searchTavily(runCtx, client, query, key, opts)
	case "brave":
		if key == "" {
			return SearchResult{}, fmt.Errorf("environment variable %s is empty (required for brave search)", opts.APIKeyEnv)
		}
		return searchBrave(runCtx, client, query, key, opts)
	case "searxng":
		return searchSearxng(runCtx, client, query, opts)
	default:
		return SearchResult{}, fmt.Errorf("unsupported search provider %q", provider)
	}
}

func searchTavily(ctx context.Context, client *http.Client, query, key string, opts SearchOptions) (SearchResult, error) {
	endpoint := providerEndpoint(opts.BaseURL, "https://api.tavily.com", "/search")
	body, err := json.Marshal(map[string]any{
		"query":          query,
		"max_results":    opts.MaxResults,
		"search_depth":   "basic",
		"include_answer": false,
	})
	if err != nil {
		return SearchResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return SearchResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := doJSON(client, req, &payload); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Query: query, Provider: "tavily"}
	for _, item := range payload.Results {
		out.Results = append(out.Results, Hit{Title: item.Title, URL: item.URL, Snippet: clipRunes(item.Content, 2000)})
	}
	trimHits(&out, opts.MaxResults)
	return out, nil
}

func searchBrave(ctx context.Context, client *http.Client, query, key string, opts SearchOptions) (SearchResult, error) {
	endpoint := providerEndpoint(opts.BaseURL, "https://api.search.brave.com/res/v1", "/web/search")
	u, err := url.Parse(endpoint)
	if err != nil {
		return SearchResult{}, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("count", fmt.Sprint(opts.MaxResults))
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SearchResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	var payload struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := doJSON(client, req, &payload); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Query: query, Provider: "brave"}
	for _, item := range payload.Web.Results {
		out.Results = append(out.Results, Hit{Title: item.Title, URL: item.URL, Snippet: clipRunes(item.Description, 2000)})
	}
	trimHits(&out, opts.MaxResults)
	return out, nil
}

func searchSearxng(ctx context.Context, client *http.Client, query string, opts SearchOptions) (SearchResult, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return SearchResult{}, fmt.Errorf("web.search.base_url is required for searxng")
	}
	endpoint := providerEndpoint(opts.BaseURL, "", "/search")
	u, err := url.Parse(endpoint)
	if err != nil {
		return SearchResult{}, err
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("safesearch", "1")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return SearchResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := doJSON(client, req, &payload); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Query: query, Provider: "searxng"}
	for _, item := range payload.Results {
		out.Results = append(out.Results, Hit{Title: item.Title, URL: item.URL, Snippet: clipRunes(item.Content, 2000)})
	}
	trimHits(&out, opts.MaxResults)
	return out, nil
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("search provider returned %s: %s", resp.Status, clipRunes(string(data), 500))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode search response: %w", err)
	}
	return nil
}

func providerEndpoint(base, fallback, suffix string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = fallback
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, suffix) {
		return base
	}
	return base + suffix
}

func trimHits(result *SearchResult, max int) {
	if len(result.Results) > max {
		result.Results = result.Results[:max]
	}
}
