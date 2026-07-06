package pipeline

import "testing"

func TestSelectForeachPaneModelFamilyDifferenceUsesAuthorModel(t *testing.T) {
	strategyPanes := []paneStrategyPane{
		{ID: "p1", ModelFamily: "cc"},
		{ID: "p2", ModelFamily: "cod"},
	}
	item := map[string]interface{}{"author_model": "cc"}

	got, _, _, err := selectForeachPane("by_model_family_difference", strategyPanes, nil, item, 0)
	if err != nil {
		t.Fatalf("selectForeachPane() error = %v", err)
	}
	if got != "p2" {
		t.Fatalf("selectForeachPane() = %q, want p2", got)
	}
}
