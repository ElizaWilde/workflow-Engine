package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/oracle-bmi/workflow-engine/internal/auth"
	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/transport/httpapi"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func TestAPI(t *testing.T) {
	h := auth.Middleware(map[string]auth.Principal{"sales": {ID: "alice", Role: auth.Sales}, "approver": {ID: "bob", Role: auth.Approver}}, httpapi.New(workflow.New(memory.New()), slog.New(slog.NewTextHandler(io.Discard, nil))))
	request := func(method, path, body, token string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	home := request(http.MethodGet, "/", "", "sales", 200)
	if home.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(home.Body.String(), `href="/healthz"`) {
		t.Fatalf("unexpected landing page: %s", home.Body.String())
	}
	request(http.MethodGet, "/unknown", "", "sales", 404)
	request(http.MethodGet, "/healthz", "", "sales", 200)
	request(http.MethodPost, "/quotes", `{}`, "", 401)
	w := request(http.MethodPost, "/quotes", `{"reference":"BMI-001","currency":"USD","amount_minor":100}`, "sales", 201)
	var q domain.Quote
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if q.Owner != "alice" || w.Header().Get("Location") != "/quotes/"+q.ID {
		t.Fatalf("unexpected quote: %+v", q)
	}
	request(http.MethodGet, "/quotes/"+q.ID, "", "sales", 200)
	request(http.MethodPut, "/quotes/"+q.ID, `{"version":1,"reference":"updated","currency":"USD","amount_minor":200}`, "sales", 200)
	request(http.MethodPost, "/quotes/"+q.ID+"/transitions", `{"version":2,"to":"issued"}`, "sales", 422)
	request(http.MethodPost, "/quotes/"+q.ID+"/actions", `{"version":2,"action":"submit"}`, "sales", 200)
	request(http.MethodPost, "/quotes/"+q.ID+"/transitions", `{"version":2,"to":"submitted"}`, "sales", 409)
	request(http.MethodPost, "/quotes/"+q.ID+"/actions", `{"version":3,"action":"approve"}`, "sales", 403)
	request(http.MethodPost, "/quotes/"+q.ID+"/actions", `{"version":3,"action":"approve"}`, "approver", 200)
	request(http.MethodGet, "/quotes/"+q.ID+"/history", "", "sales", 200)
	request(http.MethodGet, "/quotes/missing", "", "sales", 404)
	request(http.MethodPost, "/quotes", `{"unknown":true}`, "sales", 400)
	request(http.MethodPost, "/quotes", `{"reference":"x","currency":"USD","amount_minor":1,"actor":"admin"}`, "sales", 400)
	request(http.MethodPost, "/quotes", `{} {}`, "sales", 400)
	request(http.MethodPost, "/quotes", `null`, "sales", 400)
}
