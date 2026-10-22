package httpapi_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/repository/memory"
	"example.com/oracle-bmi/workflow-engine/internal/transport/httpapi"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func TestAPI(t *testing.T) {
	h := httpapi.New(workflow.New(memory.New()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	home := request(http.MethodGet, "/", "", 200)
	if home.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(home.Body.String(), `href="/healthz"`) {
		t.Fatalf("unexpected landing page: %s", home.Body.String())
	}
	request(http.MethodGet, "/unknown", "", 404)
	request(http.MethodGet, "/healthz", "", 200)
	w := request(http.MethodPost, "/quotes", `{"reference":"BMI-001","currency":"USD","amount_minor":100,"actor":"tester"}`, 201)
	var q domain.Quote
	if err := json.Unmarshal(w.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, "/quotes/"+q.ID, "", 200)
	request(http.MethodPost, "/quotes/"+q.ID+"/transitions", `{"version":1,"to":"issued","actor":"tester"}`, 422)
	request(http.MethodPost, "/quotes/"+q.ID+"/transitions", `{"version":1,"to":"submitted","actor":"tester"}`, 200)
	request(http.MethodPost, "/quotes/"+q.ID+"/transitions", `{"version":1,"to":"submitted","actor":"tester"}`, 409)
	request(http.MethodGet, "/quotes/"+q.ID+"/history", "", 200)
	request(http.MethodGet, "/quotes/missing", "", 404)
	request(http.MethodPost, "/quotes", `{"unknown":true}`, 400)
	request(http.MethodPost, "/quotes", `{} {}`, 400)
	request(http.MethodPost, "/quotes", `null`, 400)
}
