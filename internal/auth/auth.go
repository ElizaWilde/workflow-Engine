// Package auth supplies trusted request identities for the learning API.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Role string

const (
	Sales    Role = "sales"
	Approver Role = "approver"
	Admin    Role = "admin"
)

type Principal struct {
	ID   string `json:"id"`
	Role Role   `json:"role"`
}

func (p Principal) Valid() bool {
	return strings.TrimSpace(p.ID) != "" && len(p.ID) <= 200 && (p.Role == Sales || p.Role == Approver || p.Role == Admin)
}

type contextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, contextKey{}, p)
}

func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(contextKey{}).(Principal)
	return p, ok && p.Valid()
}

// ParseTokens reads a token-to-principal map. Roles and actors never come from a request body.
func ParseTokens(raw string) (map[string]Principal, error) {
	var tokens map[string]Principal
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return nil, fmt.Errorf("AUTH_TOKENS must be a JSON token-to-principal map")
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("AUTH_TOKENS must contain at least one token")
	}
	for token, p := range tokens {
		if token == "" || strings.ContainsAny(token, " \t\r\n") || !p.Valid() {
			return nil, fmt.Errorf("AUTH_TOKENS contains an invalid token, identity, or role")
		}
	}
	return tokens, nil
}


//Identify the person making a request
func Middleware(tokens map[string]Principal, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fields := strings.Fields(r.Header.Get("Authorization"))
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			if p, ok := tokens[fields[1]]; ok && p.Valid() {
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
	})
}
