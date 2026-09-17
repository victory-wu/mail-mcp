package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountBearer(t *testing.T) {
	const subkey = "host-user@example.com-550e8400-e29b-41d4-a716-446655440000"
	for _, tc := range []struct {
		name   string
		ids    []string
		header string
		query  string
		want   int
	}{
		{"valid", []string{subkey}, "Bearer " + subkey, "", 200},
		{"scheme case insensitive", []string{subkey}, "bearer " + subkey, "", 200},
		{"subkey case sensitive", []string{subkey}, "Bearer " + strings.ToUpper(subkey), "", 401},
		{"empty store", nil, "Bearer " + subkey, "", 401},
		{"empty id", []string{""}, "Bearer ", "", 401},
		{"query is not authorization", []string{subkey}, "", "?subkey=" + subkey, 401},
		{"missing", []string{subkey}, "", "", 401},
		{"wrong scheme", []string{subkey}, "Basic " + subkey, "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hit := false
			h := RequireAccountBearer(tc.ids, discardLogger(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hit = true
				if got := AuthenticatedAccount(r.Context()); got != subkey {
					t.Fatalf("account = %q", got)
				}
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "/mcp"+tc.query, nil)
			req.Header.Set("Authorization", tc.header)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want || hit != (tc.want == http.StatusOK) {
				t.Fatalf("status %d, hit %v", rec.Code, hit)
			}
			if strings.Contains(rec.Body.String(), subkey) {
				t.Fatal("response disclosed credential")
			}
			if AuthenticatedAccount(req.Context()) != "" {
				t.Fatal("modified original request context")
			}
		})
	}
}
