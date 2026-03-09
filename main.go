package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

const (
	serverPort = ":8080"
	serverAddr = "localhost" + serverPort
)

const headerProxyChain = "X-Proxy-Chain"

type ProxyHandler struct {
	logger *log.Logger
}

func NewProxyHandler() *ProxyHandler {
	return &ProxyHandler{
		logger: log.New(log.Writer(), "[PROXY] ", log.LstdFlags),
	}
}

func (h *ProxyHandler) parseTargetURL(requestPath string) (targetURL *url.URL, remainingPath string, err error) {
	cleanPath := strings.TrimPrefix(requestPath, "/")

	protocolIndex := strings.Index(cleanPath, "://")
	if protocolIndex == -1 {
		return nil, "", fmt.Errorf("invalid format: expected /http(s)://host/path")
	}

	hostStart := protocolIndex + 3
	pathIndex := strings.Index(cleanPath[hostStart:], "/")

	if pathIndex == -1 {
		targetURL, err = url.Parse(cleanPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse target URL: %w", err)
		}
		remainingPath = "/"
	} else {
		rawTargetURL := cleanPath[:hostStart+pathIndex]
		remainingPath = "/" + cleanPath[hostStart+pathIndex:]

		targetURL, err = url.Parse(rawTargetURL)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse target URL: %w", err)
		}
	}

	if targetURL.Scheme == "" {
		return nil, "", fmt.Errorf("missing scheme in target URL")
	}
	if targetURL.Host == "" {
		return nil, "", fmt.Errorf("missing host in target URL")
	}

	return targetURL, remainingPath, nil
}

// parseProxyChain splits the X-Proxy-Chain header into a slice of proxy URLs.
// Each entry is a full base URL, e.g. ["https://proxy-b.com", "https://proxy-c.com"]
func parseProxyChain(header string) []string {
	if header == "" {
		return nil
	}
	parts := strings.Split(header, ",")
	chain := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			chain = append(chain, trimmed)
		}
	}
	return chain
}

// resolveTarget determines the actual target URL and remaining path to proxy to,
// taking the proxy chain into account.
//
// Without a chain:  targetURL=somesite.com,  path=/original/path
// With a chain:     targetURL=proxy-b.com,   path=/https://somesite.com/original/path
//
//	(and the chain header is trimmed by one hop before forwarding)
func resolveTarget(
	originalTarget *url.URL,
	originalPath string,
	chain []string,
) (targetURL *url.URL, remainingPath string, forwardChain string, err error) {
	if len(chain) == 0 {
		return originalTarget, originalPath, "", nil
	}

	// Next hop is the first proxy in the chain.
	nextProxy := chain[0]
	nextProxyURL, err := url.Parse(nextProxy)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid proxy in chain %q: %w", nextProxy, err)
	}
	if nextProxyURL.Scheme == "" || nextProxyURL.Host == "" {
		return nil, "", "", fmt.Errorf("proxy chain entry must include scheme and host: %q", nextProxy)
	}

	// Build the path for the next hop:
	// next proxy expects /<scheme>://<host><path>
	// e.g. /https://somesite.com/api/foo
	encodedDestination := originalTarget.Scheme + "://" + originalTarget.Host + originalPath
	remainingPath = "/" + encodedDestination

	// Remaining chain entries (after the hop we're consuming now) get forwarded.
	if len(chain) > 1 {
		forwardChain = strings.Join(chain[1:], ", ")
	}

	return nextProxyURL, remainingPath, forwardChain, nil
}

func (h *ProxyHandler) createReverseProxy(
	targetURL *url.URL,
	remainingPath string,
	forwardChain string,
	originalQuery string,
) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.URL.Path = remainingPath
		req.URL.RawQuery = originalQuery
		req.Host = targetURL.Host

		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Origin-Host", targetURL.Host)
		req.Header.Set("X-Proxy-By", "proxygo")

		// Forward the trimmed chain to the next hop, or remove it entirely.
		if forwardChain != "" {
			req.Header.Set(headerProxyChain, forwardChain)
		} else {
			req.Header.Del(headerProxyChain)
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logger.Printf("Proxy error for %s: %v", r.URL.Path, err)
		http.Error(w, fmt.Sprintf("Proxy error: %v", err), http.StatusBadGateway)
	}

	return proxy
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Printf("Received request: %s %s", r.Method, r.URL.Path)

	targetURL, remainingPath, err := h.parseTargetURL(r.URL.Path)
	if err != nil {
		h.logger.Printf("Failed to parse target URL: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	chain := parseProxyChain(r.Header.Get(headerProxyChain))

	targetURL, remainingPath, forwardChain, err := resolveTarget(targetURL, remainingPath, chain)
	if err != nil {
		h.logger.Printf("Failed to resolve proxy chain: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if forwardChain != "" {
		h.logger.Printf("Chaining via %s, remaining chain: [%s]", targetURL.String(), forwardChain)
	} else {
		h.logger.Printf("Proxying to: %s%s", targetURL.String(), remainingPath)
	}

	proxy := h.createReverseProxy(targetURL, remainingPath, forwardChain, r.URL.RawQuery)
	proxy.ServeHTTP(w, r)
}

func main() {
	handler := NewProxyHandler()

	server := &http.Server{
		Addr:    serverPort,
		Handler: handler,
	}

	handler.logger.Printf("Proxy server starting on %s", serverAddr)
	handler.logger.Printf("Direct:  curl http://%s/https://example.com/api/endpoint", serverAddr)
	handler.logger.Printf("Chained: curl http://%s/https://example.com/api/endpoint -H 'X-Proxy-Chain: https://proxy-b.com'", serverAddr)
	handler.logger.Printf("Multi:   curl http://%s/https://example.com/api/endpoint -H 'X-Proxy-Chain: https://proxy-b.com, https://proxy-c.com'", serverAddr)

	if err := server.ListenAndServe(); err != nil {
		handler.logger.Fatalf("Server failed to start: %v", err)
	}
}
