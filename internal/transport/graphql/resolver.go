package graphql

import "example.com/oracle-bmi/workflow-engine/internal/workflow"

//go:generate sh -c "cd ../../.. && GOTOOLCHAIN=go1.23.12 go run github.com/99designs/gqlgen generate"

type Resolver struct{ Engine *workflow.Engine }
