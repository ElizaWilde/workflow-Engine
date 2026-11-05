package graphql

import "example.com/oracle-bmi/workflow-engine/internal/workflow"

//go:generate sh -c "cd ../../.. && go run github.com/99designs/gqlgen generate"

type Resolver struct{ Engine *workflow.Engine }
