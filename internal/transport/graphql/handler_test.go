package graphql_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	graph "example.com/oracle-bmi/workflow-engine/internal/transport/graphql"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

type response struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

func query(t *testing.T, h http.Handler, token, document string, variables map[string]any) response {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": document, "variables": variables})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var result response
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("GraphQL HTTP %d: %s", w.Code, w.Body.String())
	}
	return result
}

func TestGraphQLLifecycleAndErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := auth.Middleware(map[string]auth.Principal{"sales": {ID: "alice", Role: auth.Sales}, "approver": {ID: "bob", Role: auth.Approver}}, graph.New(workflow.New(memory.New()), logger))
	created := query(t, h, "sales", `mutation { createQuote(input:{reference:"Q-001",currency:"USD",amountMinor:"9223372036854775807"}) { id owner version state amountMinor } }`, nil)
	if len(created.Errors) > 0 {
		t.Fatalf("create: %+v", created)
	}
	var q struct{ ID, Owner, Version, State, AmountMinor string }
	if err := json.Unmarshal(created.Data["createQuote"], &q); err != nil {
		t.Fatal(err)
	}
	if q.Version != "1" || q.AmountMinor != "9223372036854775807" || q.Owner != "alice" || q.State != "DRAFT" {
		t.Fatalf("quote = %+v", q)
	}
	vars := map[string]any{"id": q.ID, "version": "1"}
	edited := query(t, h, "sales", `mutation($id:ID!,$version:Int64!){updateQuote(id:$id,input:{version:$version,reference:"edited",currency:"EUR",amountMinor:"2500"}){version reference}}`, vars)
	if len(edited.Errors) > 0 {
		t.Fatalf("update: %+v", edited)
	}
	action := `mutation($id:ID!,$version:Int64!,$action:QuoteAction!){executeQuoteAction(id:$id,version:$version,action:$action){state version}}`
	vars["version"], vars["action"] = "2", "ISSUE"
	result := query(t, h, "sales", action, vars)
	if len(result.Errors) != 1 || result.Errors[0].Extensions["code"] != "TRANSITION_NOT_ALLOWED" {
		t.Fatalf("illegal action: %+v", result)
	}
	vars["action"] = "SUBMIT"
	if result := query(t, h, "sales", action, vars); len(result.Errors) > 0 {
		t.Fatalf("submit: %+v", result)
	}
	if result := query(t, h, "sales", action, vars); len(result.Errors) != 1 || result.Errors[0].Extensions["code"] != "CONFLICT" {
		t.Fatalf("stale version: %+v", result)
	}
	vars["version"], vars["action"] = "3", "APPROVE"
	if result := query(t, h, "sales", action, vars); len(result.Errors) != 1 || result.Errors[0].Extensions["code"] != "FORBIDDEN" {
		t.Fatalf("sales approval: %+v", result)
	}
	if result := query(t, h, "approver", action, vars); len(result.Errors) > 0 {
		t.Fatalf("approve: %+v", result)
	}
	vars["version"], vars["action"] = "4", "ISSUE"
	if result := query(t, h, "sales", action, vars); len(result.Errors) > 0 {
		t.Fatalf("issue: %+v", result)
	}
	history := query(t, h, "sales", `query($id:ID!){quote(id:$id){state} quoteHistory(id:$id){version from to action actor quote{reference amountMinor}}}`, map[string]any{"id": q.ID})
	if len(history.Errors) > 0 {
		t.Fatalf("history: %+v", history)
	}
	var events []struct {
		From                   *string
		Actor, Action, Version string
	}
	if err := json.Unmarshal(history.Data["quoteHistory"], &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 || events[0].From != nil || events[3].Actor != "bob" || events[4].Action != "issue" {
		t.Fatalf("audit = %+v", events)
	}
	if result := query(t, h, "sales", `{ quote(id:"missing"){id} }`, nil); len(result.Errors) != 1 || result.Errors[0].Extensions["code"] != "NOT_FOUND" {
		t.Fatalf("missing: %+v", result)
	}
	for _, doc := range []string{
		`mutation { createQuote(input:{reference:"x",currency:"USD",amountMinor:"9223372036854775808"}){id} }`,
		`mutation { createQuote(input:{reference:"x",currency:"USD",amountMinor:"1.5"}){id} }`,
		`mutation { createQuote(input:{reference:"x",currency:"USD",amountMinor:"1",actor:"admin"}){id} }`,
		`{ notAField }`,
	} {
		if result := query(t, h, "sales", doc, nil); len(result.Errors) == 0 {
			t.Fatalf("accepted invalid operation: %s", doc)
		}
	}
}

type failingRepository struct{ workflow.Repository }

func (failingRepository) Get(context.Context, string) (domain.Quote, error) {
	return domain.Quote{}, errors.New("database password=secret")
}

func TestGraphQLMasksStorageFailures(t *testing.T) {
	h := graph.New(workflow.New(failingRepository{}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{ID: "a", Role: auth.Admin})))
	})
	result := query(t, wrapped, "unused", `{quote(id:"x"){id}}`, nil)
	if len(result.Errors) != 1 || result.Errors[0].Message != "internal server error" || result.Errors[0].Extensions["code"] != "INTERNAL_SERVER_ERROR" {
		t.Fatalf("leaked storage failure: %+v", result)
	}
}
