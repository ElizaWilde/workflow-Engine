package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
)

func TestTokenAuthentication(t *testing.T) {
	tokens, err := auth.ParseTokens(`{"configured-secret":{"id":"alice","role":"sales"}}`)
	if err != nil {
		t.Fatal(err)
	}
	h := auth.Middleware(tokens, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.FromContext(r.Context())
		if !ok || p.ID != "alice" || p.Role != auth.Sales {
			t.Fatalf("wrong identity: %+v", p)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		header string
		status int
	}{
		{"", 401}, {"Bearer unknown", 401}, {"Basic configured-secret", 401}, {"Bearer configured-secret extra", 401}, {"Bearer configured-secret", 204},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Authorization", tc.header)
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%q: HTTP %d", tc.header, w.Code)
		}
	}
	for _, raw := range []string{``, `null`, `{}`, `{"x":{"id":"alice","role":"superuser"}}`, `{"x y":{"id":"alice","role":"sales"}}`, `{"x":{"id":" ","role":"sales"}}`} {
		if _, err := auth.ParseTokens(raw); err == nil {
			t.Fatalf("accepted invalid config: %q", raw)
		}
	}
}
