package club

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// R32d: what the app still asked the script for, on the server. The sheet
// parts (lead magnets, the team's to-do list, the SMM plan, the channel's
// subscribers) are section writes like the others; the Telegram side (the
// channel, sending a file) is done by the gateway (app_ported.go).

const (
	SheetTodo        = "Задачи BS"
	SheetSubscribers = "Подписчики канала"
)

type portedSection func(p map[string]string, at time.Time, sheet, read func(string) *grid) error

var portedSections = map[string]portedSection{
	// swapLeadmagnetFiles: the files of two lead magnets were mixed up.
	"swapLeadmagnetFiles": func(p map[string]string, _ time.Time, sheet, read func(string) *grid) error {
		a, b := strings.TrimSpace(p["keyA"]), strings.TrimSpace(p["keyB"])
		if a == "" || b == "" {
			return errors.New("Нужны оба ключа")
		}
		if a == b {
			return errors.New("Выбраны одинаковые")
		}
		if read(SheetLeadmagnets).last() < 2 {
			return errors.New("Лист пуст")
		}
		g := sheet(SheetLeadmagnets)
		ra, rb := -1, -1
		for i := 2; i <= g.last(); i++ {
			switch g.get(i, 1) {
			case a:
				ra = i
			case b:
				rb = i
			}
		}
		if ra < 0 || rb < 0 {
			return errors.New("Чек-лист не найден")
		}
		fa, fb := g.get(ra, 3), g.get(rb, 3)
		g.set(ra, 3, fb)
		g.set(rb, 3, fa)
		return nil
	},
	// addTodo: the team's task (the script added a calendar event too; the
	// app gets a calendar link instead).
	"addTodo": func(p map[string]string, at time.Time, sheet, _ func(string) *grid) error {
		text := strings.TrimSpace(p["text"])
		if text == "" {
			return errors.New("Пустой текст")
		}
		dl := ""
		if d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(p["deadline"]), Almaty); err == nil {
			dl = d.Format("02.01.2006")
		}
		id := strings.TrimSpace(p["id"])
		if id == "" {
			id = fmt.Sprintf("t%d", at.UnixMilli())
		}
		sheet(SheetTodo).append(id, text, "open", at.In(Almaty).Format("02.01.2006 15:04"), "", dl, "")
		return nil
	},
	// smmPublished: a post of the SMM plan went to the channel.
	"smmPublished": func(p map[string]string, _ time.Time, sheet, read func(string) *grid) error {
		row, ok := jsParseInt(p["row"])
		if !ok || row < 2 || row > read(SheetSmm).last() {
			return errors.New("Bad row")
		}
		sheet(SheetSmm).set(row, 6, "Опубликовано")
		return nil
	},
	// subscriberRemoved: banned from the channel.
	"subscriberRemoved": func(p map[string]string, _ time.Time, sheet, read func(string) *grid) error {
		cid := strings.TrimSpace(p["chatId"])
		r := read(SheetSubscribers)
		for i := 2; i <= r.last(); i++ {
			if strings.TrimSpace(r.get(i, 2)) == cid {
				sheet(SheetSubscribers).set(i, 10, "Удалён с канала")
				return nil
			}
		}
		return nil // not in the old list: nothing to mark
	},
}

func init() {
	sectionHeaders[SheetTodo] = []string{"ID", "Текст", "Статус", "Дата создания", "Дата выполнения", "Дата дедлайна", "Событие в календаре"}
	for a, sheets := range map[string][]string{"swapLeadmagnetFiles": {SheetLeadmagnets}, "addTodo": {SheetTodo},
		"smmPublished": {SheetSmm}, "subscriberRemoved": {SheetSubscribers}} {
		SectionWriteActions[a] = sheets
	}
}

// PortedTeamOnly: the ported section writes only the team makes.
var PortedTeamOnly = map[string]bool{"swapLeadmagnetFiles": true, "addTodo": true, "smmPublished": true, "subscriberRemoved": true}
