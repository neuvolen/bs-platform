package club

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Editing a resident's card from the platform (team only): the settings the
// sheet keeps in «BS - резиденты дебет» — Исключение (L), Формат (O), Дата
// входа, Chat ID (N) and the partner. The values are checked and written the
// way the sheet shows them, so the server, the script and the import agree.

// ResidentEditFields: what the platform's resident card may change.
var ResidentEditFields = []string{"exception", "format", "joinedAt", "chatId", "partner"}

// EditExceptionValue: «Да» / «Нет» from a toggle (true/false, 1/0, да/нет).
func EditExceptionValue(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "да", "true", "1", "yes", "on":
		return "Да", nil
	case "нет", "false", "0", "no", "off", "":
		return "Нет", nil
	}
	return "", fmt.Errorf("исключение: «%s» — нужно Да или Нет", v)
}

// EditFormatValue: «Онлайн» or «Офлайн».
func EditFormatValue(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "онлайн", "online":
		return "Онлайн", nil
	case "офлайн", "offline", "оффлайн":
		return "Офлайн", nil
	}
	return "", fmt.Errorf("формат: «%s» — нужно Онлайн или Офлайн", v)
}

// EditJoinDate: the entry date as 30.09.2026 (also takes 2026-09-30); not in
// the future and not before the club started (2024).
func EditJoinDate(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	var d time.Time
	var err error
	for _, layout := range []string{"02.01.2006", "2006-01-02"} {
		if d, err = time.ParseInLocation(layout, v, Almaty); err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("дата входа «%s»: нужна дата в виде 30.09.2026", v)
	}
	if d.Year() < 2024 || d.After(now.In(Almaty).AddDate(0, 0, 31)) {
		return time.Time{}, fmt.Errorf("дата входа %s вне допустимого диапазона", d.Format("02.01.2006"))
	}
	return d, nil
}

// EditChatID: a Telegram chat id is digits only (empty clears it).
func EditChatID(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 || strings.HasPrefix(v, "+") {
		return "", fmt.Errorf("Chat ID «%s» не число", v)
	}
	return strconv.FormatInt(n, 10), nil
}
