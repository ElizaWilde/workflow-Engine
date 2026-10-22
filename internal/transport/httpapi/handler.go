package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
)

func New(engine *workflow.Engine, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Oracle BMI Workflow Engine</title></head>
<body>
<h1>Oracle BMI Workflow Engine</h1>
<p>The workflow API is running.</p>
<p><a href="/healthz">Check service health</a></p>
<h2>API endpoints</h2>
<ul>
<li><code>POST /quotes</code> — Create a draft quote.</li>
<li><code>GET /quotes/{id}</code> — Read a quote.</li>
<li><code>POST /quotes/{id}/transitions</code> — Change a quote's lifecycle state.</li>
<li><code>GET /quotes/{id}/history</code> — Read its audit history.</li>
</ul>
<p>Use an HTTP client to send JSON to POST endpoints. See the project README for examples.</p>
</body>
</html>`)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /quotes", func(w http.ResponseWriter, r *http.Request) {
		var in workflow.CreateInput
		if !decode(w, r, &in) {
			return
		}
		q, err := engine.Create(r.Context(), in)
		if err != nil {
			fail(w, r, logger, err)
			return
		}
		w.Header().Set("Location", "/quotes/"+q.ID)
		respond(w, http.StatusCreated, q)
	})
	mux.HandleFunc("GET /quotes/{id}", func(w http.ResponseWriter, r *http.Request) {
		q, err := engine.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, r, logger, err)
			return
		}
		respond(w, http.StatusOK, q)
	})
	mux.HandleFunc("GET /quotes/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		events, err := engine.History(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, r, logger, err)
			return
		}
		respond(w, http.StatusOK, events)
	})
	mux.HandleFunc("POST /quotes/{id}/transitions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Version int64        `json:"version"`
			To      domain.State `json:"to"`
			Actor   string       `json:"actor"`
		}
		if !decode(w, r, &in) {
			return
		}
		q, err := engine.Move(r.Context(), r.PathValue("id"), in.Version, in.To, in.Actor)
		if err != nil {
			fail(w, r, logger, err)
			return
		}
		respond(w, http.StatusOK, q)
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		respond(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		respond(w, http.StatusBadRequest, map[string]string{"error": "expected one JSON object"})
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, message := http.StatusInternalServerError, "internal server error"
	switch {
	case errors.Is(err, domain.ErrNotFound):
		status, message = http.StatusNotFound, domain.ErrNotFound.Error()
	case errors.Is(err, domain.ErrConflict):
		status, message = http.StatusConflict, domain.ErrConflict.Error()
	case errors.Is(err, domain.ErrTransition):
		status, message = http.StatusUnprocessableEntity, domain.ErrTransition.Error()
	case errors.Is(err, domain.ErrInvalid):
		status, message = http.StatusBadRequest, domain.ErrInvalid.Error()
	default:
		logger.ErrorContext(r.Context(), "request failed", "error", err)
	}
	respond(w, status, map[string]string{"error": message})
}
