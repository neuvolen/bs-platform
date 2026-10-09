package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// A resident without a board of their own fills the wheel of balance on the
// platform: their page starts a new board with empty name fields. It used to
// be refused (403 not_your_board) and the answers stayed in that browser only.
// Now the server keeps it, labelled with the resident's name; another person's
// board and a board relabelled to someone else stay refused.
func TestResidentWheelBoardKept(t *testing.T) {
	repo, db := platformTestRepo(t)
	ctx := context.Background()
	ids := []string{"rw-new1", "rw-other", "rw-own"}
	clean := func() {
		for _, id := range ids {
			_, _ = db.Pool.Exec(ctx, `DELETE FROM platform_boards WHERE id = $1`, id)
		}
	}
	clean()
	defer clean()
	_, _ = db.Pool.Exec(ctx, `INSERT INTO platform_residents (tg_id, name, active) VALUES (777000444, 'Динара Сейт', true)
		ON CONFLICT (tg_id) DO UPDATE SET name=EXCLUDED.name, active=true`)
	defer db.Pool.Exec(ctx, `DELETE FROM platform_residents WHERE tg_id = 777000444`) //nolint:errcheck
	if _, err := repo.PutBoard(ctx, "rw-other", 0, json.RawMessage(`{"id":"rw-other","name":"Разбор Асета","info":{"res":"Асет"}}`), "t"); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	ph := NewPlatformHandler(repo)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("role", "resident"); c.Set("userID", "tg:777000444") })
	r.PUT("/boards/:id", ph.PutBoard)
	put := func(id string, ver int, data string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"version": ver, "data": json.RawMessage(data)})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/boards/"+id, bytes.NewReader(body)))
		return w
	}

	// 1. New board from the resident's page: name fields empty, the wheel filled
	w := put("rw-new1", 0, `{"id":"rw-new1","name":"Новый разбор","info":{"name":"","last":"","res":""},"tests":{"wheel":{"Здоровье":7,"Семья":9}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("resident's new board refused: %d %s", w.Code, w.Body.String())
	}
	b, err := repo.GetBoard(ctx, "rw-new1")
	if err != nil || b == nil {
		t.Fatalf("board not kept: %v", err)
	}
	var got struct {
		Name string `json:"name"`
		Info struct {
			Res string `json:"res"`
		} `json:"info"`
		Tests struct {
			Wheel map[string]float64 `json:"wheel"`
		} `json:"tests"`
	}
	_ = json.Unmarshal(b.Data, &got)
	if got.Info.Res != "Динара Сейт" || got.Name != "Разбор · Динара Сейт" || got.Tests.Wheel["Здоровье"] != 7 || got.Tests.Wheel["Семья"] != 9 {
		t.Fatalf("kept board: %s", b.Data)
	}

	// 2. The next save from the same page (still no name in the fields) is accepted
	w = put("rw-new1", b.Version, `{"id":"rw-new1","name":"Новый разбор","info":{"res":""},"tests":{"wheel":{"Здоровье":8,"Семья":9}}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("second save refused: %d %s", w.Code, w.Body.String())
	}

	// 3. Another person's board: refused
	if w = put("rw-other", 1, `{"id":"rw-other","info":{"res":""},"tests":{"wheel":{"Здоровье":1}}}`); w.Code != http.StatusForbidden {
		t.Fatalf("other's board: %d", w.Code)
	}
	// 4. A new board labelled with someone else's name: refused
	if w = put("rw-own", 0, `{"id":"rw-own","info":{"res":"Асет"}}`); w.Code != http.StatusForbidden {
		t.Fatalf("board for someone else: %d", w.Code)
	}
}

func TestStampBoardResident(t *testing.T) {
	out, err := stampBoardResident(json.RawMessage(`{"name":"Мой план","info":{"city":"Алматы"}}`), "Динара")
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Name string            `json:"name"`
		Info map[string]string `json:"info"`
	}
	_ = json.Unmarshal(out, &b)
	if b.Name != "Мой план" || b.Info["res"] != "Динара" || b.Info["city"] != "Алматы" {
		t.Fatalf("stamped: %s", out)
	}
	out, _ = stampBoardResident(json.RawMessage(`{"name":""}`), "Динара")
	_ = json.Unmarshal(out, &b)
	if b.Name != "Разбор · Динара" || b.Info["res"] != "Динара" {
		t.Fatalf("stamped empty: %s", out)
	}
}
