package graphql

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/workflow"
	gql "github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func New(engine *workflow.Engine, logger *slog.Logger) http.Handler {
	srv := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{Engine: engine}}))
	srv.AddTransport(transport.POST{})
	srv.Use(extension.Introspection{})
	srv.Use(extension.FixedComplexityLimit(200))
	srv.SetParserTokenLimit(10000)
	srv.SetQueryCache(lru.New[*ast.QueryDocument](100))
	srv.SetErrorPresenter(func(ctx context.Context, err error) *gqlerror.Error {
		result := gql.DefaultErrorPresenter(ctx, err)
		code := "INTERNAL_SERVER_ERROR"
		switch {
		case errors.Is(err, domain.ErrUnauthorized):
			code = "UNAUTHENTICATED"
		case errors.Is(err, domain.ErrForbidden):
			code = "FORBIDDEN"
		case errors.Is(err, domain.ErrNotFound):
			code = "NOT_FOUND"
		case errors.Is(err, domain.ErrConflict):
			code = "CONFLICT"
		case errors.Is(err, domain.ErrInvalid):
			code = "BAD_USER_INPUT"
		case errors.Is(err, domain.ErrTransition):
			code = "TRANSITION_NOT_ALLOWED"
		case errors.Is(err, domain.ErrRule):
			code = "BUSINESS_RULE_FAILED"
		default:
			var public *gqlerror.Error
			if errors.As(err, &public) && public.Err == nil {
				return result
			}
			logger.ErrorContext(ctx, "GraphQL request failed", "error", err)
			result.Message = "internal server error"
		}
		result.Extensions = map[string]any{"code": code}
		return result
	})
	srv.SetRecoverFunc(func(ctx context.Context, value any) error {
		logger.ErrorContext(ctx, "GraphQL resolver panic", "panic", value)
		return errors.New("internal server error")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		srv.ServeHTTP(w, r)
	})
}
