package http

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Пакет данных приложения по роли. Раньше любой, кто открыл приложение
// (в том числе лид), получал долги, оплаты и PL всего клуба: роль считало само
// приложение. Теперь сервер отдаёт каждому только его часть.
//   команда   всё как есть;
//   резидент  список клуба без чужих денег, свои штрафы, колесо и задачи;
//             без PL, кассы, лидов, SMM и контент-плана;
//   лид       только имена и формат резидентов и свои материалы.

var residentPublic = map[string]bool{"name": true, "chatId": true, "format": true, "isFired": true, "isExcluded": true,
	"isAdmin": true, "partner": true, "dateIn": true, "months": true, "startDate": true}

func (g *AppGateway) forUser(uid int64, body []byte) []byte {
	if _, team := g.Admins[uid]; team {
		return body
	}
	var d map[string]json.RawMessage
	if json.Unmarshal(body, &d) != nil {
		return body
	}
	data, isWrapped := d, false
	var wrapped map[string]json.RawMessage
	if raw, ok := d["data"]; ok && json.Unmarshal(raw, &wrapped) == nil && wrapped["residents"] != nil {
		data, isWrapped = wrapped, true
	}
	var residents []map[string]any
	_ = json.Unmarshal(data["residents"], &residents)
	me := ""
	idStr := strconv.FormatInt(uid, 10)
	for _, r := range residents {
		if strings.TrimSpace(anyString(r["chatId"])) == idStr && r["isFired"] != true {
			me, _ = r["name"].(string)
		}
	}
	mine := func(name any) bool { return me != "" && strings.TrimSpace(anyString(name)) == strings.TrimSpace(me) }
	put := func(k string, v any) {
		if _, ok := data[k]; ok {
			b, _ := json.Marshal(v)
			data[k] = b
		}
	}
	filterList := func(k string, keep func(m map[string]any) bool) {
		var list []map[string]any
		if json.Unmarshal(data[k], &list) != nil {
			return
		}
		out := []map[string]any{}
		for _, m := range list {
			if keep(m) {
				out = append(out, m)
			}
		}
		put(k, out)
	}
	var pub []map[string]any
	for _, r := range residents {
		if me != "" && mine(r["name"]) {
			pub = append(pub, r)
			continue
		}
		p := map[string]any{}
		for k, v := range r {
			if residentPublic[k] && (me != "" || k == "name" || k == "format") {
				p[k] = v
			}
		}
		pub = append(pub, p)
	}
	if pub == nil {
		pub = []map[string]any{}
	}
	put("residents", pub)
	put("debet", map[string]any{})
	put("totalDebt", 0)
	put("monthlyPL", map[string]any{"ok": false})
	put("leads", map[string]any{"leads": []any{}})
	put("problems", map[string]any{"problems": []any{}, "total": 0})
	put("smm", map[string]any{"items": []any{}})
	put("contentPlan", map[string]any{"items": []any{}})
	put("wheelSummary", map[string]any{})
	var wheels map[string]json.RawMessage
	if json.Unmarshal(data["wheelAll"], &wheels) == nil {
		own := map[string]json.RawMessage{}
		if me != "" {
			if w, ok := wheels[me]; ok {
				own[me] = w
			}
		}
		put("wheelAll", own)
	}
	filterList("fines", func(m map[string]any) bool { return mine(m["name"]) })
	var tasks struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if json.Unmarshal(data["resTasks"], &tasks) == nil {
		out := []map[string]any{}
		for _, t := range tasks.Tasks {
			if mine(t["res"]) || mine(t["name"]) || mine(t["resident"]) {
				out = append(out, t)
			}
		}
		put("resTasks", map[string]any{"tasks": out})
	}
	if me == "" { // a lead: nothing about the club's people
		for _, k := range []string{"fines", "logs", "schedule", "doneMeetings", "adminProfiles", "checklistSchedule"} {
			put(k, []any{})
		}
	}
	if isWrapped {
		b, _ := json.Marshal(data)
		d["data"] = b
	}
	out, err := json.Marshal(d)
	if err != nil {
		return body
	}
	return out
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), `"`)
}
