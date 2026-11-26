// Package web fetches and searches the web for the aglet agent.
package web

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/zhimoe/aglet/internal/version"
)

// FetchOptions configures an HTTP fetch.
type FetchOptions struct {
	MaxBytes     int
	Timeout      time.Duration
	AllowPrivate bool
	AllowedHosts []string
	UserAgent    string
}

// FetchResult is the readable result of fetching a URL.
type FetchResult struct {
	URL         string
	FinalURL    string
	Title       string
	ContentType string
	Content     string
	Status      int
	Bytes       int
	Truncated   bool
}

const (
	defaultFetchBytes = 1024 * 1024
	maxFetchBytes     = 5 * 1024 * 1024
	defaultFetchChars = 40_000
	maxFetchChars     = 200_000
)

// Fetch retrieves a URL and converts HTML to readable text.
func Fetch(ctx context.Context, rawURL string, opts FetchOptions, maxChars int) (FetchResult, error) {
	var result FetchResult
	if len(rawURL) > 8*1024 {
		return result, fmt.Errorf("url is too long")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return result, fmt.Errorf("parse url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return result, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" || parsed.User != nil {
		return result, fmt.Errorf("url must have a host and no credentials")
	}
	if err := checkAllowedHost(parsed.Hostname(), opts.AllowedHosts); err != nil {
		return result, err
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultFetchBytes
	}
	if opts.MaxBytes > maxFetchBytes {
		opts.MaxBytes = maxFetchBytes
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.Timeout > 2*time.Minute {
		opts.Timeout = 2 * time.Minute
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "aglet/" + version.Version
	}
	if maxChars <= 0 {
		maxChars = defaultFetchChars
	}
	if maxChars > maxFetchChars {
		maxChars = maxFetchChars
	}

	client := fetchClient(opts)
	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,application/json;q=0.8,*/*;q=0.1")
	resp, err := client.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	body, truncated, err := readLimited(resp.Body, opts.MaxBytes)
	if err != nil {
		return result, err
	}
	result.URL = rawURL
	result.FinalURL = resp.Request.URL.String()
	result.Status = resp.StatusCode
	result.Bytes = len(body)
	result.Truncated = truncated

	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = "text/plain"
	}
	result.ContentType = mediaType
	switch {
	case mediaType == "text/html" || mediaType == "application/xhtml+xml":
		title, text, err := HTMLToText(body)
		if err != nil {
			return result, err
		}
		result.Title = title
		content, cut := truncateRunes(text, maxChars)
		result.Content = content
		result.Truncated = result.Truncated || cut
	case isPlainText(mediaType):
		content, cut := truncateRunes(string(body), maxChars)
		result.Content = content
		result.Truncated = result.Truncated || cut
	case resp.StatusCode >= 400:
		// Keep the status for the model even when the error body is binary.
		return result, nil
	default:
		return result, fmt.Errorf("unsupported content type %q", mediaType)
	}
	return result, nil
}

func fetchClient(opts FetchOptions) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return nil
			}
			if !opts.AllowPrivate && isBlockedIP(ip) {
				return fmt.Errorf("blocked private address %s", ip)
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to unsupported scheme %q", req.URL.Scheme)
			}
			if req.URL.User != nil {
				return fmt.Errorf("redirect must not contain credentials")
			}
			return checkAllowedHost(req.URL.Hostname(), opts.AllowedHosts)
		},
	}
}

func readLimited(r io.Reader, max int) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(max)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

func isPlainText(mediaType string) bool {
	switch {
	case strings.HasPrefix(mediaType, "text/"):
		return true
	case mediaType == "application/json",
		mediaType == "application/xml",
		mediaType == "application/javascript",
		mediaType == "application/x-javascript":
		return true
	default:
		return false
	}
}

func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, cidr := range blockedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

var blockedCIDRs = func() []*net.IPNet {
	ranges := []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "2001:db8::/32"}
	out := make([]*net.IPNet, 0, len(ranges))
	for _, raw := range ranges {
		if _, network, err := net.ParseCIDR(raw); err == nil {
			out = append(out, network)
		}
	}
	return out
}()

func checkAllowedHost(host string, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := strings.TrimPrefix(pattern, "*")
			if host == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(host, suffix) {
				return nil
			}
			continue
		}
		if host == pattern {
			return nil
		}
	}
	return fmt.Errorf("host %q is not in web.fetch.allowed_hosts", host)
}

func truncateRunes(s string, max int) (string, bool) {
	r := []rune(s)
	if len(r) <= max {
		return s, false
	}
	return string(r[:max]) + "\n...[truncated]", true
}

func clipRunes(s string, max int) string {
	out, _ := truncateRunes(s, max)
	return out
}
