package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/expr-lang/expr"
)

// DSL Types and Builders

// Expect creates a new expectation with an expression
func Expect(expression string) *Expectation {
	return &Expectation{
		expect: []string{expression},
		not:    []string{},
	}
}

// Expectation represents a single request expectation
type Expectation struct {
	expect  []string // Expressions that must ALL evaluate to true (AND)
	not     []string // Expressions that must ALL evaluate to false (AND)
	returns int
	forever bool
}

// Expect adds another expression that must be true (AND semantics)
func (e *Expectation) Expect(expression string) *Expectation {
	e.expect = append(e.expect, expression)
	return e
}

// Not adds an expression that must NOT be true (AND semantics)
func (e *Expectation) Not(expression string) *Expectation {
	e.not = append(e.not, expression)
	return e
}

// Returns specifies how many events to return
func (e *Expectation) Returns(count int) *Expectation {
	e.returns = count
	return e
}

// Forever makes this expectation repeat indefinitely
func (e *Expectation) Forever() *Expectation {
	e.forever = true
	return e
}

// RequestContext provides request data to expr evaluations
type RequestContext struct {
	Method  string            `expr:"method"`
	Path    string            `expr:"path"`
	Query   map[string]string `expr:"query"`
	Headers map[string]string `expr:"headers"`
	Since   *time.Time        `expr:"since"` // Parsed since parameter if present
	After   string            `expr:"after"`
}

// BuildRequestContext creates context from HTTP request
func BuildRequestContext(r *http.Request) RequestContext {
	ctx := RequestContext{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   make(map[string]string),
		Headers: make(map[string]string),
	}

	// Extract query parameters
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			ctx.Query[key] = values[0]
		}
	}

	// Extract headers
	for key, values := range r.Header {
		if len(values) > 0 {
			ctx.Headers[key] = values[0]
		}
	}

	// Parse since if present
	if sinceStr, ok := ctx.Query["since"]; ok {
		if t, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			ctx.Since = &t
		}
	}

	// Set after directly
	ctx.After = ctx.Query["after"]

	return ctx
}

// Sequence creates an ordered sequence of expectations
type Sequence struct {
	expectations []*Expectation
	current      int
	failed       bool
	mu           sync.Mutex
}

// InOrder creates a new sequence
func InOrder(expectations ...*Expectation) *Sequence {
	return &Sequence{expectations: expectations}
}

// Validate checks if request matches current expectation
func (s *Sequence) Validate(r *http.Request) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failed {
		return 0, fmt.Errorf("sequence in error state")
	}

	if s.current >= len(s.expectations) {
		s.failed = true
		return 0, fmt.Errorf("no more expectations")
	}

	expect := s.expectations[s.current]
	ctx := BuildRequestContext(r)

	// Evaluate all expect expressions (AND semantics)
	for _, expectExpr := range expect.expect {
		if expectExpr == "" {
			continue
		}

		program, err := expr.Compile(expectExpr, expr.Env(RequestContext{}))
		if err != nil {
			s.failed = true
			return 0, fmt.Errorf("failed to compile expect expression: %v", err)
		}

		result, err := expr.Run(program, ctx)
		if err != nil {
			s.failed = true
			return 0, fmt.Errorf("failed to evaluate expect expression: %v", err)
		}

		if ok, _ := result.(bool); !ok {
			s.failed = true
			return 0, fmt.Errorf("request %d failed expectation: %s", s.current+1, expectExpr)
		}
	}

	// Evaluate all not expressions (AND semantics - all must be false)
	for _, notExpr := range expect.not {
		if notExpr == "" {
			continue
		}

		program, err := expr.Compile(notExpr, expr.Env(RequestContext{}))
		if err != nil {
			s.failed = true
			return 0, fmt.Errorf("failed to compile not expression: %v", err)
		}

		result, err := expr.Run(program, ctx)
		if err != nil {
			s.failed = true
			return 0, fmt.Errorf("failed to evaluate not expression: %v", err)
		}

		if ok, _ := result.(bool); ok {
			s.failed = true
			return 0, fmt.Errorf("request %d failed not condition: %s", s.current+1, notExpr)
		}
	}

	eventCount := expect.returns

	// Advance unless repeating forever
	if !expect.forever {
		s.current++
	}

	return eventCount, nil
}

// Mock server implementation

type OktaMock struct {
	sequence   *Sequence
	authHeader string
	baseEvent  map[string]interface{}
}

func NewOktaMock(authHeader string, sequence *Sequence) *OktaMock {
	return &OktaMock{
		authHeader: authHeader,
		sequence:   sequence,
		baseEvent:  createBaseEvent(),
	}
}

func (m *OktaMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Basic validations
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("Authorization") != m.authHeader {
		m.sequence.mu.Lock()
		m.sequence.failed = true
		m.sequence.mu.Unlock()
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Validate time format if present
	if sinceParam := r.URL.Query().Get("since"); sinceParam != "" {
		if _, err := time.Parse(time.RFC3339, sinceParam); err != nil {
			m.sequence.mu.Lock()
			m.sequence.failed = true
			m.sequence.mu.Unlock()
			http.Error(w, "Invalid since time: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Check sequence expectations
	eventCount, err := m.sequence.Validate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Build response
	events := make([]map[string]interface{}, eventCount)
	for i := 0; i < eventCount; i++ {
		events[i] = m.baseEvent
	}

	// Send response with Link headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"self\"", buildSelfURL(r)))
	w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"next\"", buildNextURL(r)))
	json.NewEncoder(w).Encode(events)
}

func buildSelfURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s", scheme, r.Host, r.RequestURI)
}

func buildNextURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	u, _ := url.Parse(fmt.Sprintf("%s://%s%s", scheme, r.Host, r.URL.Path))
	q := url.Values{}

	// Copy all params except 'since'
	for key, values := range r.URL.Query() {
		if key != "since" {
			for _, value := range values {
				q.Add(key, value)
			}
		}
	}

	// Add incremented 'after' param
	afterVal := 1
	if current := r.URL.Query().Get("after"); current != "" {
		if parsed, err := strconv.Atoi(current); err == nil {
			afterVal = parsed + 1
		}
	}
	q.Set("after", strconv.Itoa(afterVal))

	u.RawQuery = q.Encode()
	return u.String()
}

func createBaseEvent() map[string]interface{} {
	return map[string]interface{}{
		"uuid":            "test-uuid-123",
		"published":       time.Now().UTC().Format(time.RFC3339),
		"eventType":       "user.session.start",
		"version":         "0",
		"severity":        "INFO",
		"legacyEventType": "core.user_auth.login_success",
		"displayMessage":  "User login to Okta",
		"actor": map[string]interface{}{
			"id":          "test-user-123",
			"type":        "User",
			"alternateId": "test@example.com",
			"displayName": "Test User",
		},
		"client": map[string]interface{}{
			"userAgent": map[string]interface{}{
				"rawUserAgent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
				"os":           "Mac OS X",
				"browser":      "CHROME",
			},
			"zone":      "null",
			"device":    "Computer",
			"id":        "test-client-123",
			"ipAddress": "192.0.2.1",
			"geographicalContext": map[string]interface{}{
				"city":    "San Francisco",
				"state":   "California",
				"country": "United States",
			},
		},
		"outcome": map[string]interface{}{
			"result": "SUCCESS",
		},
		"target": []map[string]interface{}{
			{
				"id":          "test-target-123",
				"type":        "AppInstance",
				"alternateId": "Test App",
				"displayName": "Test Application",
			},
		},
		"transaction": map[string]interface{}{
			"type": "WEB",
			"id":   "test-transaction-123",
		},
		"debugContext": map[string]interface{}{
			"debugData": map[string]interface{}{
				"requestId":  "test-request-123",
				"requestUri": "/app/test/sso/saml",
			},
		},
		"authenticationContext": map[string]interface{}{
			"authenticationProvider": "OKTA_AUTHENTICATION_PROVIDER",
			"credentialProvider":     "OKTA_CREDENTIAL_PROVIDER",
			"credentialType":         "PASSWORD",
			"issuer":                 nil,
			"interface":              nil,
			"authenticationStep":     0,
			"externalSessionId":      "test-session-123",
		},
		"securityContext": map[string]interface{}{
			"asNumber": 13335,
			"asOrg":    "Cloudflare Inc",
			"isp":      "Cloudflare Inc",
			"domain":   "example.com",
			"isProxy":  false,
		},
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// LoggingMiddleware wraps an http.Handler with request/response logging
type LoggingMiddleware struct {
	handler http.Handler
}

func NewLoggingMiddleware(handler http.Handler) *LoggingMiddleware {
	return &LoggingMiddleware{handler: handler}
}

func (lm *LoggingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Log request
	log.Printf("→ REQUEST: %s %s", r.Method, r.URL.String())
	log.Printf("  Headers: %v", r.Header)
	
	// Read and log body if present
	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, _ = io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		if len(bodyBytes) > 0 {
			log.Printf("  Body: %s", string(bodyBytes))
		}
	}

	// Create response recorder to capture response
	recorder := &ResponseRecorder{
		ResponseWriter: w,
		statusCode:     200,
		body:          &bytes.Buffer{},
	}

	// Call the wrapped handler
	lm.handler.ServeHTTP(recorder, r)

	// Log response
	log.Printf("← RESPONSE: %d", recorder.statusCode)
	log.Printf("  Headers: %v", recorder.Header())
	if recorder.body.Len() > 0 {
		log.Printf("  Body: %s", recorder.body.String())
	}
}

// ResponseRecorder captures response data for logging
type ResponseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       *bytes.Buffer
}

func (rr *ResponseRecorder) WriteHeader(code int) {
	rr.statusCode = code
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *ResponseRecorder) Write(data []byte) (int, error) {
	rr.body.Write(data)
	return rr.ResponseWriter.Write(data)
}

func main() {
	authHeader := flag.String("auth", "", "Expected Authorization header value")
	port := flag.String("port", "8080", "Port to listen on")
	flag.Parse()

	if *authHeader == "" {
		log.Fatal("Authorization header must be specified with -auth flag")
	}

	// Define the expected sequence using DSL with expr expressions
	sequence := InOrder(
		// First request: must have 'since' param, no 'after'
		Expect(`"since" in query`).Not(`"after" in query`).Returns(2),

		// Second request: must have 'after' param, no 'since'
		// Example of multiple conditions:
		Expect(`"after" in query`).
			Expect(`query.limit == "2"`).
			Not(`"since" in query`).
			Returns(1),

		// Third request: must have 'since' param, no 'after'
		Expect(`"since" in query`).Not(`"after" in query`).Returns(1),

		// Fourth request: must have 'since' param, no 'after'
		Expect(`"since" in query`).Not(`"after" in query`).Returns(1),

		// Subsequent requests: must have 'since', no 'after', return empty
		Expect(`"since" in query`).
			Not(`"after" in query`).
			Returns(0).
			Forever(),
	)

	mock := NewOktaMock(*authHeader, sequence)

	// Wrap handlers with logging middleware
	http.Handle("/api/v1/logs", NewLoggingMiddleware(mock))
	http.HandleFunc("/health", handleHealth)

	fmt.Printf("Mock Okta API server listening on port %s\n", *port)
	fmt.Printf("Expected Authorization header: %s\n", *authHeader)

	log.Fatal(http.ListenAndServe(":"+*port, nil))
}
