package http

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bnursik/business_surgery_backend/internal/content"
)

func TestMergeDirectCompetitors(t *testing.T) {
	list := mktDirectList()
	if len(list) < 10 {
		t.Fatalf("competitors_direct.json: %d", len(list))
	}
	// The team's entries stay, a match by key in the name is not duplicated.
	doc := map[string]any{"competitors": []any{map[string]any{"name": "Платформа Маргулана Сейсембая"}, map[string]any{"name": "Монста (правка команды)"}}}
	if n := mergeDirectCompetitors(doc, list); n != len(list)-2 {
		t.Fatalf("added %d", n)
	}
	if mergeDirectCompetitors(doc, list) != 0 {
		t.Fatal("second merge added again")
	}
	b, _ := json.Marshal(doc)
	if strings.Contains(string(b), `"key"`) || !strings.Contains(string(b), "Монста (правка команды)") {
		t.Fatalf("doc: %s", b)
	}
	// The first version already carries all of them.
	var m map[string]any
	_ = json.Unmarshal(content.Marketing, &m)
	if mergeDirectCompetitors(m, list) != 0 {
		t.Fatal("marketing.json misses direct competitors")
	}
}
