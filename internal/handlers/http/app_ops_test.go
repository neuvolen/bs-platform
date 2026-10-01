package http

import (
	"context"
	"net/url"
	"testing"

	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

type fakeOps struct{ ops []pg.ClubOp }

func (f *fakeOps) LogOp(_ context.Context, op pg.ClubOp) error { f.ops = append(f.ops, op); return nil }

func TestAppJournalsChanges(t *testing.T) {
	g, _, r, now := newGateway(t)
	fo := &fakeOps{}
	g.Ops = fo
	init := makeInitData(testBotToken, 453800951, "Рустам", *now)
	get(r, url.Values{"action": {"getBotCache"}, "_tg": {init}})
	get(r, url.Values{"action": {"checkUserRole"}, "_tg": {init}})
	get(r, url.Values{"action": {"addFine"}, "_tg": {init}, "name": {"Асет"}, "amount": {"10000"}})
	if len(fo.ops) != 1 {
		t.Fatalf("only changes are journaled: %+v", fo.ops)
	}
	op := fo.ops[0]
	if op.Action != "addFine" || op.Source != "app" || op.TgID != 453800951 || op.Who != "Рустам" || op.Params["name"] != "Асет" || op.Params["_tg"] != "" {
		t.Fatalf("op: %+v", op)
	}
}
