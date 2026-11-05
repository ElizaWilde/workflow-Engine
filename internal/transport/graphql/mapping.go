package graphql

import (
	"strings"

	"example.com/oracle-bmi/workflow-engine/internal/domain"
	"example.com/oracle-bmi/workflow-engine/internal/transport/graphql/model"
	"example.com/oracle-bmi/workflow-engine/internal/transport/graphql/scalar"
)

func stateModel(state domain.State) model.QuoteState {
	return model.QuoteState(strings.ToUpper(string(state)))
}

func quoteModel(q domain.Quote) *model.Quote {
	return &model.Quote{ID: q.ID, Reference: q.Reference, Currency: q.Currency, AmountMinor: scalar.Int64(q.AmountMinor), Owner: q.Owner, State: stateModel(q.State), Version: scalar.Int64(q.Version), CreatedAt: q.CreatedAt, UpdatedAt: q.UpdatedAt}
}
