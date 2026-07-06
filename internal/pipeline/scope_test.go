package pipeline

import "testing"

func TestVariableScopeRestoresShadowedLoopLocals(t *testing.T) {
	vars := map[string]interface{}{
		"loop.item":  "outer",
		"loop.index": 2,
		"keep":       "unchanged",
	}

	scope := CaptureVariableScope(vars, loopScopeKeys("file")...)
	vars["loop.file"] = "inner-file"
	vars["loop.item"] = "inner"
	delete(vars, "loop.index")

	scope.Restore(vars)

	if vars["loop.item"] != "outer" {
		t.Fatalf("loop.item = %v, want outer", vars["loop.item"])
	}
	if vars["loop.index"] != 2 {
		t.Fatalf("loop.index = %v, want 2", vars["loop.index"])
	}
	if _, ok := vars["loop.file"]; ok {
		t.Fatalf("loop.file still present after restore: %v", vars["loop.file"])
	}
	if vars["keep"] != "unchanged" {
		t.Fatalf("unrelated variable changed: %v", vars["keep"])
	}
}

func TestLoopExecutorNestedScopesRestoreOuterItem(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("sess"))
	executor.state = &ExecutionState{
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	loopExec := NewLoopExecutor(executor)

	outerScope := loopExec.pushLoopVars("item", map[string]interface{}{"id": "outer"}, 0, 1)
	assertSubstituted(t, executor.state, "${item.id}", "outer")

	innerScope := loopExec.pushLoopVars("item", map[string]interface{}{"id": "inner"}, 0, 1)
	assertSubstituted(t, executor.state, "${item.id}", "inner")

	loopExec.popLoopVars(innerScope)
	assertSubstituted(t, executor.state, "${item.id}", "outer")

	loopExec.popLoopVars(outerScope)
	sub := NewSubstitutor(executor.state, "sess", "wf")
	if _, err := sub.Substitute("${item.id}"); err == nil {
		t.Fatal("expected item reference to be unavailable after popping outer scope")
	}
}

func TestLoopExecutorAliasScopeRestoresOuterAlias(t *testing.T) {
	executor := NewExecutor(DefaultExecutorConfig("sess"))
	executor.state = &ExecutionState{
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps:      map[string]StepResult{},
	}
	loopExec := NewLoopExecutor(executor)

	outerScope := loopExec.pushLoopVars("file", "outer.go", 0, 1)
	assertSubstituted(t, executor.state, "${loop.file}", "outer.go")
	assertSubstituted(t, executor.state, "${item}", "outer.go")

	innerScope := loopExec.pushLoopVars("file", "inner.go", 0, 1)
	assertSubstituted(t, executor.state, "${loop.file}", "inner.go")
	assertSubstituted(t, executor.state, "${item}", "inner.go")

	loopExec.popLoopVars(innerScope)
	assertSubstituted(t, executor.state, "${loop.file}", "outer.go")
	assertSubstituted(t, executor.state, "${item}", "outer.go")

	loopExec.popLoopVars(outerScope)
	sub := NewSubstitutor(executor.state, "sess", "wf")
	if _, err := sub.Substitute("${loop.file}"); err == nil {
		t.Fatal("expected alias reference to be unavailable after popping outer scope")
	}
}

func TestBranchVariableSnapshotRestoresMutations(t *testing.T) {
	state := &ExecutionState{Variables: map[string]interface{}{
		"global": "before",
		"keep":   "same",
	}}

	snapshot := captureAllVariables(state.Variables)
	state.Variables["global"] = "branch-local"
	state.Variables["temporary"] = "created in branch"
	delete(state.Variables, "keep")

	restoreAllVariables(state, snapshot)

	if state.Variables["global"] != "before" {
		t.Fatalf("global = %v, want before", state.Variables["global"])
	}
	if state.Variables["keep"] != "same" {
		t.Fatalf("keep = %v, want same", state.Variables["keep"])
	}
	if _, ok := state.Variables["temporary"]; ok {
		t.Fatalf("temporary variable leaked after branch restore: %v", state.Variables["temporary"])
	}
}

func assertSubstituted(t *testing.T, state *ExecutionState, input, want string) {
	t.Helper()
	sub := NewSubstitutor(state, "sess", "wf")
	got, err := sub.Substitute(input)
	if err != nil {
		t.Fatalf("Substitute(%q) returned error: %v", input, err)
	}
	if got != want {
		t.Fatalf("Substitute(%q) = %q, want %q", input, got, want)
	}
}
