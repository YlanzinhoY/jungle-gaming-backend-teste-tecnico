package composition

import (
	"testing"

	"go.uber.org/fx"
)

func TestModuleDependencyGraph(t *testing.T) {
	t.Parallel()

	if err := fx.ValidateApp(Module()); err != nil {
		t.Fatalf("Fx dependency graph is invalid: %v", err)
	}
}
