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

// ProxyHandler handles HTTP proxy requests
type ProxyHandler struct {
	logger *log.Logger
}

// NewProxyHandler creates a new proxy handler
func NewProxyHandler() *ProxyHandler {
	return &ProxyHandler{
		logger: log.New(log.Writer(), "[PROXY] ", log.LstdFlags),
	}
}

// parseTargetURL extracts the target URL and remaining path from the request
func (h *ProxyHandler) parseTargetURL(requestPath string) (targetURL *url.URL, remainingPath string, err error) {
	// Remove leading slash: /https://example.com/api/foo -> https://example.com/api/foo
	cleanPath := strings.TrimPrefix(requestPath, "/")

	// Find the protocol separator (://)
	protocolIndex := strings.Index(cleanPath, "://")
	if protocolIndex == -1 {
		return nil, "", fmt.Errorf("invalid format: expected /http(s)://host/path")
	}

	// Find the first slash after the host part
	// Start searching after the protocol://host part
	hostStart := protocolIndex + 3 // Skip "://"
	pathIndex := strings.Index(cleanPath[hostStart:], "/")

	if pathIndex == -1 {
		// No path component, the entire string is the target URL
		targetURL, err = url.Parse(cleanPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse target URL: %w", err)
		}
		remainingPath = "/"
	} else {
		// Split at the path boundary
		rawTargetURL := cleanPath[:hostStart+pathIndex] // e.g., "https://example.com"
		remainingPath = cleanPath[hostStart+pathIndex:] // e.g., "/api/foo"

		// Parse the target URL
		targetURL, err = url.Parse(rawTargetURL)
		if err != nil {
			return nil, "", fmt.Errorf("failed to parse target URL: %w", err)
		}
	}

	// Validate the parsed URL
	if targetURL.Scheme == "" {
		return nil, "", fmt.Errorf("missing scheme in target URL")
	}
	if targetURL.Host == "" {
		return nil, "", fmt.Errorf("missing host in target URL")
	}

	return targetURL, remainingPath, nil
}

// createReverseProxy creates a reverse proxy for the given target URL
func (h *ProxyHandler) createReverseProxy(targetURL *url.URL, remainingPath string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Customize the request director
	proxy.Director = func(req *http.Request) {
		// Set the target URL components
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.URL.Path = remainingPath
		req.URL.RawQuery = req.URL.RawQuery

		// Set the Host header to the target host
		req.Host = targetURL.Host

		// Add proxy headers for debugging and tracking
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Origin-Host", targetURL.Host)
		req.Header.Set("X-Proxy-By", "proxygo")
	}

	// Handle proxy errors
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.logger.Printf("Proxy error for %s: %v", r.URL.Path, err)
		http.Error(w, fmt.Sprintf("Proxy error: %v", err), http.StatusBadGateway)
	}

	return proxy
}

// ServeHTTP handles incoming HTTP requests
func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Printf("Received request: %s %s", r.Method, r.URL.Path)

	// Parse the target URL from the request path
	targetURL, remainingPath, err := h.parseTargetURL(r.URL.Path)
	if err != nil {
		h.logger.Printf("Failed to parse target URL: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	h.logger.Printf("Proxying to: %s%s", targetURL.String(), remainingPath)

	// Create and serve the reverse proxy
	proxy := h.createReverseProxy(targetURL, remainingPath)
	proxy.ServeHTTP(w, r)
}

func main() {
	// Create the proxy handler
	handler := NewProxyHandler()

	// Set up the HTTP server
	server := &http.Server{
		Addr:    serverPort,
		Handler: handler,
	}

	// Start the server
	handler.logger.Printf("Proxy server starting on %s", serverAddr)
	handler.logger.Printf("Usage: http://%s/https://example.com/api/endpoint", serverAddr)

	if err := server.ListenAndServe(); err != nil {
		handler.logger.Fatalf("Server failed to start: %v", err)
	}
}
