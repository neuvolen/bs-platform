package video

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Template: a Reels recipe the owner picks.
type Template struct {
	ID, Name, Desc string
	Fit            string // crop: fill 9:16 from the centre; blur: whole frame on a blurred copy
	Captions       bool
	Punch          bool
	Cut            bool
}

// Templates: what «Шаблон» offers in SMM → «Видео».
var Templates = []Template{
	{ID: "talk", Name: "Говорящая голова", Desc: "Кадр 9:16 по центру, паузы вырезаны, субтитры по словам, наезды камеры на акцентах", Fit: "crop", Captions: true, Punch: true, Cut: true},
	{ID: "broll", Name: "Б-ролл с подписями", Desc: "Горизонтальный кадр целиком на размытом фоне, субтитры, без наездов, паузы остаются", Fit: "blur", Captions: true},
	{ID: "clean", Name: "Чистая склейка", Desc: "Кадр 9:16, паузы вырезаны, без субтитров и наездов: хук и концовка остаются", Fit: "crop", Cut: true},
}

func TemplateByID(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// Options: the template and what the owner changed in it.
type Options struct {
	Template string `json:"template"`
	Hook     string `json:"hook"`
	CTA      string `json:"cta"`
	Captions *bool  `json:"captions,omitempty"`
	Punch    *bool  `json:"punch,omitempty"`
	Cut      *bool  `json:"cut,omitempty"`
}

// DefaultCTA: the outro line.
const DefaultCTA = "Ссылка в шапке профиля: 99 чек-листов"

func (o Options) resolve() (Template, error) {
	t, ok := TemplateByID(o.Template)
	if !ok {
		return t, fmt.Errorf("нет шаблона %q", o.Template)
	}
	if o.Captions != nil {
		t.Captions = *o.Captions
	}
	if o.Punch != nil {
		t.Punch = *o.Punch
	}
	if o.Cut != nil {
		t.Cut = *o.Cut
	}
	return t, nil
}

// Tools: what a render needs from the server.
type Tools struct {
	FF      Runner
	ASR     func(ctx context.Context, wav string) ([]Seg, error) // nil: no captions
	ASRNote string                                               // why there is no ASR
	Font    []byte                                               // Manrope ExtraBold TTF
	Logo    []byte                                               // white logo PNG
	Style   Style
}

// Input: one render.
type Input struct {
	Clips    []string // the uploaded files, in order
	Music    string   // "" or a track
	Opts     Options
	MaxInput float64 // seconds of clips allowed in total
}

// Result: the finished Reels.
type Result struct {
	Out, Cover string
	Dur        float64 // seconds, with the outro
	InDur      float64 // seconds of clips
	Cut        float64 // seconds of pauses cut
	Words      int
	Text       string // what was said (for the post caption)
	Notes      []string
}

const (
	outroDur = 2.5
	fps      = 30
)

// ErrTooLong: the clips are longer than allowed.
var ErrTooLong = errors.New("клипы слишком длинные")

var safePathRe = regexp.MustCompile(`^[A-Za-z0-9/_.-]+$`)

func f2(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

func between(sp []Span) string {
	var parts []string
	for _, s := range sp {
		parts = append(parts, "between(t,"+f2(s.A)+","+f2(s.B)+")")
	}
	return strings.Join(parts, "+")
}

// Render makes the Reels in work (a fresh folder the caller removes).
// prog gets the stage name and the share done (0..1).
func Render(ctx context.Context, in Input, t Tools, work string, prog func(stage string, done float64)) (*Result, error) {
	if prog == nil {
		prog = func(string, float64) {}
	}
	if len(in.Clips) == 0 {
		return nil, errors.New("нет клипов")
	}
	if !safePathRe.MatchString(work) {
		return nil, errors.New("папка задания с недопустимыми знаками")
	}
	tpl, err := in.Opts.resolve()
	if err != nil {
		return nil, err
	}
	st := t.Style
	if st.Font == "" {
		st = DefaultStyle
	}
	res := &Result{}
	// stage weights: probe .02, clips .30, pauses .10, speech .18, outro .03, final .35, cover .02
	at := 0.0
	step := func(name string, w float64) func(float64) {
		base := at
		at += w
		prog(name, base)
		return func(x float64) {
			if x < 0 {
				x = 0
			} else if x > 1 {
				x = 1
			}
			prog(name, base+w*x)
		}
	}

	// 1. what the clips are
	p := step("Читаем клипы", 0.02)
	var media []Media
	for i, c := range in.Clips {
		m, err := Probe(ctx, t.FF, c)
		if err != nil {
			return nil, fmt.Errorf("клип %d: %v", i+1, err)
		}
		if !m.HasVideo {
			return nil, fmt.Errorf("клип %d: в файле нет видео", i+1)
		}
		if m.Dur <= 0.2 {
			return nil, fmt.Errorf("клип %d: слишком короткий", i+1)
		}
		media = append(media, m)
		res.InDur += m.Dur
		p(float64(i+1) / float64(len(in.Clips)))
	}
	if in.MaxInput > 0 && res.InDur > in.MaxInput+0.5 {
		return nil, fmt.Errorf("%w (%.0f с, можно до %.0f с)", ErrTooLong, res.InDur, in.MaxInput)
	}
	fontDir := filepath.Join(work, "fonts")
	if err := os.MkdirAll(fontDir, 0o755); err != nil {
		return nil, err
	}
	if len(t.Font) > 0 {
		if err := os.WriteFile(filepath.Join(fontDir, "Manrope-ExtraBold.ttf"), t.Font, 0o644); err != nil {
			return nil, err
		}
	}

	// 2. every clip to 1080×1920, 30 fps, stereo 48 kHz
	p = step("Готовим кадр 9:16", 0.30)
	var parts []string
	done := 0.0
	for i, c := range in.Clips {
		m := media[i]
		out := filepath.Join(work, fmt.Sprintf("n%02d.mkv", i))
		var vf string
		vertical := m.W > 0 && m.H > 0 && float64(m.W)/float64(m.H) < 0.7
		if tpl.Fit == "blur" && !vertical {
			vf = "[0:v]split[a][b];[a]scale=270:480:force_original_aspect_ratio=increase,crop=270:480,boxblur=12:2,scale=1080:1920,eq=brightness=-0.12[bg];" +
				"[b]scale=1080:1920:force_original_aspect_ratio=decrease[fg];[bg][fg]overlay=(W-w)/2:(H-h)/2"
		} else {
			vf = "[0:v]scale=1080:1920:force_original_aspect_ratio=increase,crop=1080:1920"
		}
		vf += ",fps=" + strconv.Itoa(fps) + ",format=yuv420p,setsar=1[v]"
		args := []string{"-i", c}
		amap := "0:a:0"
		if !m.HasAudio {
			args = append(args, "-f", "lavfi", "-t", f2(m.Dur), "-i", "anullsrc=r=48000:cl=stereo")
			amap = "1:a:0"
		}
		args = append(args, "-filter_complex", vf, "-map", "[v]", "-map", amap, "-t", f2(m.Dur),
			"-af", "aresample=48000,aformat=sample_fmts=s16:channel_layouts=stereo",
			"-c:v", "libx264", "-preset", "ultrafast", "-crf", "17", "-c:a", "pcm_s16le", out)
		base := done
		if _, err := t.FF.Run(ctx, args, func(s float64) { p((base + s) / res.InDur) }); err != nil {
			return nil, fail(ctx, fmt.Sprintf("клип %d не перекодировался", i+1), err)
		}
		done += m.Dur
		parts = append(parts, out)
		p(done / res.InDur)
	}
	joined := parts[0]
	if len(parts) > 1 {
		list := filepath.Join(work, "list.txt")
		var b strings.Builder
		for _, x := range parts {
			b.WriteString("file '" + strings.ReplaceAll(x, "'", `'\''`) + "'\n")
		}
		if err := os.WriteFile(list, []byte(b.String()), 0o644); err != nil {
			return nil, err
		}
		joined = filepath.Join(work, "joined.mkv")
		if _, err := t.FF.Run(ctx, []string{"-f", "concat", "-safe", "0", "-i", list, "-c", "copy", joined}, nil); err != nil {
			return nil, fail(ctx, "клипы не склеились", err)
		}
	}
	total := res.InDur

	// 3. pauses
	p = step("Вырезаем паузы", 0.10)
	cut := joined
	var keep []Span
	if tpl.Cut {
		out, err := t.FF.Run(ctx, []string{"-i", joined, "-vn", "-af", "silencedetect=noise=-35dB:d=0.5", "-f", "null", "-"}, func(s float64) { p(0.4 * s / total) })
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		keep = KeepSpans(ParseSilences(out, total), total, 0.08)
		kd := spansDur(keep)
		switch {
		case len(keep) == 0 || kd < 1.5 || kd < 0.3*total:
			res.Notes = append(res.Notes, "Паузы не вырезаны: в записи почти нет речи")
			keep = nil
		case total-kd < 0.3:
			keep = nil // nothing worth cutting
		default:
			ex := between(keep)
			cut = filepath.Join(work, "cut.mkv")
			args := []string{"-i", joined, "-filter_complex",
				"[0:v]select='" + ex + "',setpts=N/FRAME_RATE/TB[v];[0:a]aselect='" + ex + "',asetpts=N/SR/TB[a]",
				"-map", "[v]", "-map", "[a]", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "17", "-c:a", "pcm_s16le", cut}
			if _, err := t.FF.Run(ctx, args, func(s float64) { p(0.4 + 0.6*s/kd) }); err != nil {
				return nil, fail(ctx, "паузы не вырезались", err)
			}
			res.Cut = total - kd
			total = kd
		}
	}
	p(1)

	// 4. speech → captions
	p = step("Распознаём речь", 0.18)
	var pages []Page
	switch {
	case !tpl.Captions:
	case t.ASR == nil:
		n := t.ASRNote
		if n == "" {
			n = "распознавание речи на сервере выключено"
		}
		res.Notes = append(res.Notes, "Без субтитров: "+n)
	default:
		wav := filepath.Join(work, "speech.wav")
		if _, err := t.FF.Run(ctx, []string{"-i", cut, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav}, nil); err != nil {
			return nil, fail(ctx, "звук для распознавания не извлёкся", err)
		}
		p(0.1)
		segs, err := t.ASR(ctx, wav)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			res.Notes = append(res.Notes, "Без субтитров: "+err.Error())
		} else {
			ws := Words(segs)
			pages = Pages(ws)
			res.Words = len(ws)
			res.Text = Plain(segs)
			if len(ws) == 0 {
				res.Notes = append(res.Notes, "Речь не распознана: субтитров нет")
			}
		}
	}
	ass := filepath.Join(work, "captions.ass")
	hook := strings.TrimSpace(in.Opts.Hook)
	if err := os.WriteFile(ass, []byte(ASS(st, pages, hook)), 0o644); err != nil {
		return nil, err
	}
	p(1)

	// 5. outro card: black, the logo, the CTA
	p = step("Концовка", 0.03)
	outro := filepath.Join(work, "outro.mkv")
	cta := strings.TrimSpace(in.Opts.CTA)
	if cta == "" {
		cta = DefaultCTA
	}
	oass := filepath.Join(work, "outro.ass")
	if err := os.WriteFile(oass, []byte(OutroASS(st, cta, outroDur)), 0o644); err != nil {
		return nil, err
	}
	oargs := []string{"-f", "lavfi", "-i", "color=c=black:s=1080x1920:r=" + strconv.Itoa(fps) + ":d=" + f2(outroDur)}
	ofc := "[0:v]"
	if len(t.Logo) > 0 {
		logo := filepath.Join(work, "logo.png")
		if err := os.WriteFile(logo, t.Logo, 0o644); err != nil {
			return nil, err
		}
		oargs = append(oargs, "-loop", "1", "-t", f2(outroDur), "-i", logo)
		ofc = "[1:v]scale=560:-1,format=rgba[l];[0:v][l]overlay=(W-w)/2:620:shortest=1"
	}
	ain := "1"
	if len(t.Logo) > 0 {
		ain = "2"
	}
	oargs = append(oargs, "-f", "lavfi", "-t", f2(outroDur), "-i", "anullsrc=r=48000:cl=stereo",
		"-filter_complex", ofc+",ass="+oass+":fontsdir="+fontDir+",fade=in:st=0:d=0.25,format=yuv420p,setsar=1[v]",
		"-map", "[v]", "-map", ain+":a", "-t", f2(outroDur), "-c:v", "libx264", "-preset", "ultrafast", "-crf", "17", "-c:a", "pcm_s16le", outro)
	if _, err := t.FF.Run(ctx, oargs, nil); err != nil {
		return nil, fail(ctx, "концовка не собралась", err)
	}
	p(1)

	// 6. the Reels: punch-ins, captions and hook, outro, music under the voice, -14 LUFS
	p = step("Собираем Reels", 0.35)
	var zoom []Span
	if tpl.Punch {
		if len(pages) > 0 {
			zoom = Emphasis(pages)
		} else if len(keep) > 1 { // no captions: every other jump cut
			off := 0.0
			for i, k := range keep {
				d := k.B - k.A
				if i%2 == 1 && d >= 0.6 && len(zoom) < 40 {
					zoom = append(zoom, Span{off, off + d})
				}
				off += d
			}
		}
	}
	args := []string{"-i", cut, "-i", outro}
	if in.Music != "" {
		args = append(args, "-stream_loop", "-1", "-i", in.Music)
	}
	var fc strings.Builder
	if len(zoom) > 0 {
		fc.WriteString("[0:v]split[v0][v1];[v1]scale=1210:2150,crop=1080:1920[vz];[v0][vz]overlay=enable='" + between(zoom) + "'[vp];[vp]")
	} else {
		fc.WriteString("[0:v]")
	}
	fc.WriteString("ass=" + ass + ":fontsdir=" + fontDir + "[mv];")
	fc.WriteString("[mv][0:a][1:v][1:a]concat=n=2:v=1:a=1[cv][ca];")
	if in.Music != "" {
		fc.WriteString("[ca]loudnorm=I=-16:TP=-2:LRA=11,aresample=48000,asplit=2[vo][sc];" +
			"[2:a]aresample=48000,aformat=channel_layouts=stereo,loudnorm=I=-26:TP=-3:LRA=11,aresample=48000[m];" +
			"[m][sc]sidechaincompress=threshold=0.03:ratio=8:attack=20:release=400[md];" +
			"[vo][md]amix=inputs=2:duration=first:normalize=0,")
	} else {
		fc.WriteString("[ca]")
	}
	fc.WriteString("loudnorm=I=-14:TP=-1:LRA=11,aresample=48000[a]")
	res.Dur = total + outroDur
	res.Out = filepath.Join(work, "out.mp4")
	args = append(args, "-filter_complex", fc.String(), "-map", "[cv]", "-map", "[a]",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "21", "-maxrate", "8M", "-bufsize", "16M",
		"-profile:v", "high", "-level", "4.2", "-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps), "-g", "60",
		"-c:a", "aac", "-b:a", "160k", "-ar", "48000", "-ac", "2", "-movflags", "+faststart", "-t", f2(res.Dur), res.Out)
	if _, err := t.FF.Run(ctx, args, func(s float64) { p(s / res.Dur) }); err != nil {
		return nil, fail(ctx, "Reels не собрался", err)
	}
	p(1)

	// 7. cover: the hook frame (or the first second)
	p = step("Обложка", 0.02)
	res.Cover = filepath.Join(work, "cover.png")
	ss := 0.8
	if hook == "" && total > 3 {
		ss = 1.5
	}
	if _, err := t.FF.Run(ctx, []string{"-ss", f2(ss), "-i", res.Out, "-frames:v", "1", "-update", "1", res.Cover}, nil); err != nil {
		return nil, fail(ctx, "обложка не сохранилась", err)
	}
	p(1)
	return res, nil
}

// fail: a short Russian reason plus ffmpeg's last line.
func fail(ctx context.Context, what string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%s: %v", what, err)
}
