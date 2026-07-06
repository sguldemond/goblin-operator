package detection

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

// Matcher is a compiled CEL program evaluating a boolean over `object`.
type Matcher struct {
	program cel.Program
}

// Compile builds a Matcher from a CEL expression. The expression sees one
// variable, `object`, of dynamic type (the target as map[string]any), and must
// return a bool.
func Compile(expr string) (*Matcher, error) {
	env, err := cel.NewEnv(cel.Variable("object", cel.DynType))
	if err != nil {
		return nil, fmt.Errorf("cel env: %w", err)
	}
	ast, iss := env.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("compile: %w", iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("program: %w", err)
	}
	return &Matcher{program: prg}, nil
}

// Eval runs the program against object. A runtime error (e.g. missing field
// without a has() guard) or a non-bool result is returned as an error.
func (m *Matcher) Eval(object map[string]any) (bool, error) {
	out, _, err := m.program.Eval(map[string]any{"object": object})
	if err != nil {
		return false, fmt.Errorf("eval: %w", err)
	}
	b, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("expression did not evaluate to bool (got %T)", out.Value())
	}
	return b, nil
}
