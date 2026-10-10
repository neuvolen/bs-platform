package content

import (
	"embed"
	"encoding/json"
)

// R82: «В стикеры добавь мемные стикеры, ещё актуальные». 42 своих
// рисунка (персонаж «Пульс» в чёрно-белом стиле BS, подписи Manrope), без
// чужих мемов и фото людей; по категориям Деньги, Команда, Продажи, Клуб,
// Собственник. Сервер кладёт их в общий набор клуба (bs_stickerpack_srv).

//go:embed stickers_meme/*.png stickers_meme/stickers.json
var memeFS embed.FS

// MemeSticker is one sticker of the pack.
type MemeSticker struct {
	ID   string `json:"id"`
	Cat  string `json:"cat"`
	Name string `json:"name"`
}

// MemeStickers lists the pack with each sticker's PNG.
func MemeStickers() ([]MemeSticker, map[string][]byte, error) {
	b, err := memeFS.ReadFile("stickers_meme/stickers.json")
	if err != nil {
		return nil, nil, err
	}
	var list []MemeSticker
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, nil, err
	}
	files := make(map[string][]byte, len(list))
	for _, s := range list {
		png, err := memeFS.ReadFile("stickers_meme/" + s.ID + ".png")
		if err != nil {
			return nil, nil, err
		}
		files[s.ID] = png
	}
	return list, files, nil
}
