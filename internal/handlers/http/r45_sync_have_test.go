package http

import (
	"strings"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// R45: on open the page names the sections it holds at the server's version;
// those are not sent again (the seed always is: a resident's cut of it
// depends on more than its version).
func TestR45SyncOmitsHeldDocs(t *testing.T) {
	docs := []pg.PlatformDoc{
		{Key: "bs_tools", Version: 12, Value: strings.Repeat("x", 100)},
		{Key: "bs_guides", Version: 5, Value: "g"},
		{Key: "bs_diag", Version: 3, Deleted: true},
		{Key: platformSeedKey, Version: 7, Value: "{}"},
		{Key: "bs_contacts", Version: 2, Value: "c"},
	}
	out, kept := omitHeldDocs(docs, "bs_tools.12,bs_guides.4,bs_diag.3,bs_seed.7,bad key.1,bs_contacts.x,.5,bs_contacts")
	var left []string
	for _, d := range out {
		left = append(left, d.Key)
	}
	if strings.Join(kept, ",") != "bs_tools" || strings.Join(left, ",") != "bs_guides,bs_diag,bs_seed,bs_contacts" {
		t.Fatalf("kept %v, sent %v", kept, left)
	}
	if out, kept := omitHeldDocs(docs, ""); len(out) != len(docs) || len(kept) != 0 {
		t.Fatalf("no have: everything goes")
	}
}
