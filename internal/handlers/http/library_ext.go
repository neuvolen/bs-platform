package http

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/bnursik/business_surgery_backend/internal/content"
	pg "github.com/bnursik/business_surgery_backend/internal/repository/pg"
)

// Расширение библиотеки: content.LibExt() (новые диагнозы, инструменты,
// вопросы) один раз на версию добавляется в документы клуба bs_diag,
// bs_tools и bs_questions. Правила:
//   - пункты команды не меняются и не удаляются, добавляется только то,
//     чего нет ни по id, ни по названию;
//   - пункт, который сервер уже добавлял (в любой версии), второй раз не
//     добавляется, даже если команда его удалила;
//   - bs_diag и bs_tools трогаем, только когда платформа уже сохранила свою
//     базу (bs_libver >= 3): иначе страница при первом входе заменит их своими
//     исходными данными. Пока условия нет, документ ждёт следующего прохода.
//
// Состояние: server doc lib_ext_state = {version, done:{key:true}, added:{id:true}}.

const (
	libExtStateKey = "lib_ext_state"
	libExtMinLib   = 3 // LIB_VERSION страницы, с которой bs_diag/bs_tools берутся из хранилища
	libExtEvery    = 6 * time.Hour
)

type libExtState struct {
	Version string          `json:"version"`
	Done    map[string]bool `json:"done"`
	Added   map[string]bool `json:"added"`
}

// libNorm: название для сравнения (регистр, ё, знаки и пробелы не важны).
func libNorm(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	var b strings.Builder
	sp := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if sp && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			sp = false
		} else {
			sp = true
		}
	}
	return b.String()
}

func (h *PlatformAI) clubDocInt(ctx context.Context, key string) int {
	d, err := h.repo.GetDoc(ctx, "club", key)
	if err != nil || d == nil || d.Deleted {
		return 0
	}
	n, _ := strconv.Atoi(strings.Trim(strings.TrimSpace(d.Value), `"`))
	return n
}

// MergeLibExt adds the shipped extension to the club docs; returns how many
// items each doc got this time.
func (h *PlatformAI) MergeLibExt(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	if h.repo == nil {
		return out, nil
	}
	ext, err := content.LibExt()
	if err != nil {
		return out, err
	}
	st, base := libExtState{}, 0
	if d, err := h.repo.GetDoc(ctx, "server", libExtStateKey); err == nil && d != nil {
		base = d.Version
		_ = json.Unmarshal([]byte(d.Value), &st)
	}
	if st.Added == nil {
		st.Added = map[string]bool{}
	}
	if st.Version != ext.Version || st.Done == nil {
		st.Version, st.Done = ext.Version, map[string]bool{}
	}
	changed := false
	libReady := h.clubDocInt(ctx, "bs_libver") >= libExtMinLib
	for _, key := range []string{"bs_diag", "bs_tools", "bs_questions"} {
		if st.Done[key] || (key != "bs_questions" && !libReady) {
			continue
		}
		n, ready, err := h.mergeLibDoc(ctx, key, ext, st.Added)
		if err != nil {
			log.Printf("library ext %s: %v", key, err)
			continue
		}
		if ready {
			st.Done[key], changed = true, true
		}
		if n > 0 {
			out[key], changed = n, true
		}
	}
	if changed {
		val, _ := json.Marshal(st)
		if _, err := h.repo.PutDoc(ctx, "server", libExtStateKey, base, string(val), false, "server:library"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// mergeLibDoc adds missing items to one doc. ready=false: the doc is not
// there yet (the page has not saved it), try later.
func (h *PlatformAI) mergeLibDoc(ctx context.Context, key string, ext content.LibExtSet, added map[string]bool) (int, bool, error) {
	for try := 0; try < 4; try++ {
		d, err := h.repo.GetDoc(ctx, "club", key)
		if err != nil {
			return 0, false, err
		}
		if d == nil || d.Deleted || strings.TrimSpace(d.Value) == "" {
			return 0, false, nil
		}
		var val []byte
		n := 0
		newly := map[string]bool{}
		if key == "bs_questions" {
			q := map[string][]string{}
			if json.Unmarshal([]byte(d.Value), &q) != nil || len(q) == 0 {
				return 0, false, nil
			}
			have := map[string]bool{}
			for _, list := range q {
				for _, x := range list {
					have[libNorm(x)] = true
				}
			}
			for _, organ := range libExtKeys(ext.Questions) {
				for _, x := range ext.Questions[organ] {
					id := "q|" + libNorm(x)
					if added[id] || have[libNorm(x)] {
						continue
					}
					q[organ] = append(q[organ], x)
					have[libNorm(x)], newly[id] = true, true
					n++
				}
			}
			val, _ = json.Marshal(q)
		} else {
			var list []map[string]any
			if json.Unmarshal([]byte(d.Value), &list) != nil || len(list) == 0 {
				return 0, false, nil
			}
			src := ext.Diag
			if key == "bs_tools" {
				src = ext.Tools
			}
			ids, titles := map[string]bool{}, map[string]bool{}
			for _, it := range list {
				if s, _ := it["id"].(string); s != "" {
					ids[s] = true
				}
				if s, _ := it["title"].(string); s != "" {
					titles[libNorm(s)] = true
				}
			}
			for _, it := range src {
				id, _ := it["id"].(string)
				title, _ := it["title"].(string)
				if id == "" || title == "" || added[id] || ids[id] || titles[libNorm(title)] {
					continue
				}
				cp := make(map[string]any, len(it))
				for k, v := range it {
					cp[k] = v
				}
				list = append(list, cp)
				ids[id], titles[libNorm(title)], newly[id] = true, true, true
				n++
			}
			val, _ = json.Marshal(list)
		}
		if n == 0 {
			return 0, true, nil
		}
		if _, err := h.repo.PutDoc(ctx, "club", key, d.Version, string(val), false, "server:library"); err != nil {
			if err == pg.ErrPlatformConflict {
				continue
			}
			return 0, false, err
		}
		for id := range newly {
			added[id] = true
		}
		log.Printf("library ext: %s +%d", key, n)
		return n, true, nil
	}
	return 0, false, pg.ErrPlatformConflict
}

func libExtKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LibExtLoop merges at start and re-checks every few hours until every doc
// got this version (a doc the page has not saved yet waits).
func (h *PlatformAI) LibExtLoop(ctx context.Context) {
	t := time.NewTimer(90 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, time.Minute)
		if n, err := h.MergeLibExt(c); err != nil || len(n) > 0 {
			log.Printf("library ext: %v err=%v", n, err)
		}
		cancel()
		t.Reset(libExtEvery)
	}
}
