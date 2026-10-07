// Package funnelvideo: the videos of the lead funnel shipped with the server
// (R55, «Видео в воронке»: Маркетинг → SMM → Воронка).
//
// How to add a video:
//
//   - in the platform: Маркетинг → SMM → «Видео в воронке» → «Загрузить видео»
//     (MP4 H.264 up to 20 MB, vertical 720×1280 is best for Telegram), then
//     pick the step of the funnel it goes to;
//   - or in the repo: put <name>.mp4 here (up to 8 MB: ffmpeg -i in.mp4
//     -vf scale=720:1280 -c:v libx264 -b:v 1500k -maxrate 1800k -bufsize
//     3000k -c:a aac -b:a 96k -movflags +faststart <name>.mp4) and describe it
//     in videos.json: {file, title, topic, step, caption}. At the next start
//     the server copies it into Postgres (platform_files) once, with that
//     step; later changes in the platform win (the file is not copied again,
//     and a video deleted in the platform is not brought back).
//
// Steps (handlers/http/funnel_video.go, fvSteps): start (soon after /start,
// when the 99 checklists were given), d1, d3, d7, d10, d14 (with the
// warm-up touch of that day), booked (booked the express-разбор), offer
// (after the разбор, with the club offer). One video per step is sent to a
// lead, once; the first switched-on video of the step wins.
package funnelvideo

import "embed"

//go:embed videos.json *.mp4
var FS embed.FS
