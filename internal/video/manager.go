package video

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Limits of the Reels editor (documented in the README, R54).
const (
	MaxUpload    = 500 << 20 // one file
	MaxChunk     = 16 << 20  // one PUT
	MaxUploads   = 1536 << 20
	MaxClips     = 10
	MaxInputSec  = 180.0
	DefTimeout   = 30 * time.Minute
	KeepJobs     = 12
	UploadMaxAge = 24 * time.Hour
	JobMaxAge    = 14 * 24 * time.Hour
)

// Upload: a file coming in by chunks.
type Upload struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Kind    string    `json:"kind"` // clip | music
	Size    int64     `json:"size"`
	Got     int64     `json:"got"`
	Created time.Time `json:"created"`
}

// Job: one Reels render.
type Job struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"` // queued | running | done | failed
	Stage    string    `json:"stage,omitempty"`
	Pct      int       `json:"pct"`
	Error    string    `json:"error,omitempty"`
	Opts     Options   `json:"opts"`
	Clips    []string  `json:"clips"` // upload ids
	Music    string    `json:"music,omitempty"`
	Names    []string  `json:"names,omitempty"`
	By       string    `json:"by,omitempty"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
	Dur      float64   `json:"dur,omitempty"`
	InDur    float64   `json:"inDur,omitempty"`
	Cut      float64   `json:"cut,omitempty"`
	Words    int       `json:"words,omitempty"`
	Text     string    `json:"text,omitempty"`
	Notes    []string  `json:"notes,omitempty"`
	Size     int64     `json:"size,omitempty"`
	FileID   string    `json:"fileId,omitempty"`  // the Reels kept in Postgres (survives a deploy)
	CoverID  string    `json:"coverId,omitempty"` // and its cover
}

// Manager keeps uploads and jobs on disk and renders one job at a time.
type Manager struct {
	Dir     string
	Tools   func(ctx context.Context) (Tools, error) // ffmpeg may need a download first
	Timeout time.Duration
	MaxIn   float64
	// Store (optional) keeps the result elsewhere (platform_files); ids or "".
	Store func(ctx context.Context, j *Job, out, cover string) (fileID, coverID string)
	// Render is replaceable in tests; nil means Render.
	Render func(ctx context.Context, in Input, t Tools, work string, prog func(string, float64)) (*Result, error)
	Now    func() time.Time
	// MaxBytes (R55): the folder never keeps more than this (uploads and
	// finished Reels together, VIDEO_MAX_MB); past it the oldest finished
	// Reels go first, then the oldest uploads no job waits for. 0: no cap.
	MaxBytes int64

	mu      sync.Mutex
	jobs    map[string]*Job
	uploads map[string]*Upload
	queue   chan string
	cancel  map[string]context.CancelFunc
	busy    int // jobs rendering now (never above 1)
	maxBusy int // for the tests
}

var idRe = regexp.MustCompile(`^[a-f0-9]{16,32}$`)

func ValidID(id string) bool { return idRe.MatchString(id) }

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewManager loads what is on disk and starts the worker.
func NewManager(dir string) (*Manager, error) {
	m := &Manager{Dir: dir, Timeout: DefTimeout, MaxIn: MaxInputSec, Now: time.Now,
		jobs: map[string]*Job{}, uploads: map[string]*Upload{}, queue: make(chan string, 64), cancel: map[string]context.CancelFunc{}}
	for _, d := range []string{"up", "jobs", "work"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			return nil, err
		}
	}
	os.RemoveAll(filepath.Join(dir, "work")) // half-done renders of the last run
	os.MkdirAll(filepath.Join(dir, "work"), 0o755)
	m.load()
	return m, nil
}

// Start runs the worker until ctx ends; and the hourly cleanup.
func (m *Manager) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-m.queue:
				m.run(ctx, id)
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			m.Cleanup()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (m *Manager) indexPath() string { return filepath.Join(m.Dir, "index.json") }

type index struct {
	Jobs    []*Job    `json:"jobs"`
	Uploads []*Upload `json:"uploads"`
}

func (m *Manager) load() {
	b, err := os.ReadFile(m.indexPath())
	if err != nil {
		return
	}
	var ix index
	if json.Unmarshal(b, &ix) != nil {
		return
	}
	for _, u := range ix.Uploads {
		if u != nil && ValidID(u.ID) && fileOK(m.upPath(u.ID)) {
			if st, _ := os.Stat(m.upPath(u.ID)); st != nil {
				u.Got = st.Size()
			}
			m.uploads[u.ID] = u
		}
	}
	for _, j := range ix.Jobs {
		if j == nil || !ValidID(j.ID) {
			continue
		}
		if j.Status == "queued" || j.Status == "running" {
			j.Status, j.Error, j.Stage = "failed", "Сервер перезапустился во время сборки: нажмите «Собрать заново»", ""
		}
		m.jobs[j.ID] = j
	}
}

// save writes the index; the caller holds m.mu.
func (m *Manager) save() {
	ix := index{}
	for _, j := range m.jobs {
		ix.Jobs = append(ix.Jobs, j)
	}
	for _, u := range m.uploads {
		ix.Uploads = append(ix.Uploads, u)
	}
	b, _ := json.Marshal(ix)
	tmp := m.indexPath() + ".part"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, m.indexPath())
	}
}

func (m *Manager) upPath(id string) string  { return filepath.Join(m.Dir, "up", id+".bin") }
func (m *Manager) jobDir(id string) string  { return filepath.Join(m.Dir, "jobs", id) }
func (m *Manager) OutPath(id string) string { return filepath.Join(m.jobDir(id), "out.mp4") }
func (m *Manager) CoverPath(id string) string {
	return filepath.Join(m.jobDir(id), "cover.png")
}

func fileOK(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

// Free: bytes free on the disk of Dir (-1 when unknown).
func (m *Manager) Free() int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(m.Dir, &st) != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// UserError: a reason to show as is.
type UserError struct{ Msg string }

func (e UserError) Error() string { return e.Msg }

func uerr(f string, a ...any) error { return UserError{fmt.Sprintf(f, a...)} }

// NewUpload reserves a file of size bytes.
func (m *Manager) NewUpload(name, kind string, size int64) (*Upload, error) {
	if kind != "clip" && kind != "music" {
		return nil, uerr("Неизвестный тип файла")
	}
	if size <= 0 {
		return nil, uerr("Файл пустой")
	}
	if size > MaxUpload {
		return nil, uerr("Файл больше 500 МБ: сожмите его или обрежьте")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var used int64
	for _, u := range m.uploads {
		used += u.Size
	}
	if used+size > MaxUploads {
		m.mu.Unlock()
		m.Cleanup()
		m.mu.Lock()
		used = 0
		for _, u := range m.uploads {
			used += u.Size
		}
		if used+size > MaxUploads {
			return nil, uerr("На сервере уже лежат загрузки на 1,5 ГБ: удалите старые ролики или подождите сутки")
		}
	}
	if f := m.Free(); f >= 0 && f < size*3+(256<<20) {
		return nil, uerr("На диске сервера мало места: свободно %d МБ, для этого файла нужно около %d МБ", f>>20, (size*3+(256<<20))>>20)
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	u := &Upload{ID: newID(), Name: name, Kind: kind, Size: size, Created: m.Now()}
	f, err := os.Create(m.upPath(u.ID))
	if err != nil {
		return nil, err
	}
	f.Close()
	m.uploads[u.ID] = u
	m.save()
	return u, nil
}

// Chunk writes a piece at off. A repeated piece (a retry) is accepted;
// a gap is refused with the size the server has.
func (m *Manager) Chunk(id string, off int64, r io.Reader) (*Upload, error) {
	m.mu.Lock()
	u := m.uploads[id]
	if u == nil {
		m.mu.Unlock()
		return nil, uerr("Загрузка не найдена: начните заново")
	}
	got := u.Got
	m.mu.Unlock()
	if off > got || off < 0 {
		return u, ErrGap
	}
	f, err := os.OpenFile(m.upPath(id), os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxChunk+1))
	if n > MaxChunk {
		return nil, uerr("Кусок больше 16 МБ")
	}
	if off+n > u.Size {
		_ = f.Truncate(got)
		return nil, uerr("Пришло больше, чем заявлено")
	}
	if err != nil {
		_ = f.Truncate(got)
		return nil, err
	}
	m.mu.Lock()
	if off+n > u.Got {
		u.Got = off + n
	}
	if u.Got == u.Size {
		m.save()
	}
	cp := *u
	m.mu.Unlock()
	return &cp, nil
}

// ErrGap: the chunk does not continue the file.
var ErrGap = errors.New("gap")

func (m *Manager) Upload(id string) *Upload {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u := m.uploads[id]; u != nil {
		cp := *u
		return &cp
	}
	return nil
}

func (m *Manager) DeleteUpload(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if (j.Status == "queued" || j.Status == "running") && (contains(j.Clips, id) || j.Music == id) {
			return // in use
		}
	}
	delete(m.uploads, id)
	os.Remove(m.upPath(id))
	m.save()
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Submit queues a render.
func (m *Manager) Submit(clips []string, music string, o Options, by string) (*Job, error) {
	if len(clips) == 0 {
		return nil, uerr("Добавьте хотя бы один клип")
	}
	if len(clips) > MaxClips {
		return nil, uerr("Не больше %d клипов за раз", MaxClips)
	}
	if _, err := o.resolve(); err != nil {
		return nil, uerr("Выберите шаблон")
	}
	if r := []rune(o.Hook); len(r) > 70 {
		return nil, uerr("Хук длиннее 70 знаков: на экране он не прочитается за 1,5 секунды")
	}
	if r := []rune(o.CTA); len(r) > 80 {
		return nil, uerr("Призыв в концовке длиннее 80 знаков")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for _, id := range append(append([]string{}, clips...), music) {
		if id == "" {
			continue
		}
		u := m.uploads[id]
		if u == nil {
			return nil, uerr("Файл не найден на сервере (загрузки хранятся сутки): добавьте его заново")
		}
		if u.Got != u.Size {
			return nil, uerr("Файл «%s» ещё загружается", u.Name)
		}
		if id != music {
			names = append(names, u.Name)
		}
	}
	waiting := 0
	for _, j := range m.jobs {
		if j.Status == "queued" || j.Status == "running" {
			waiting++
		}
	}
	if waiting >= 3 {
		return nil, uerr("В очереди уже 3 ролика: дождитесь, пока соберутся")
	}
	j := &Job{ID: newID(), Status: "queued", Opts: o, Clips: clips, Music: music, Names: names, By: by, Created: m.Now()}
	m.jobs[j.ID] = j
	m.save()
	m.queue <- j.ID
	cp := *j
	return &cp, nil
}

// Retry queues a failed job again (its uploads must still be there).
func (m *Manager) Retry(id string) (*Job, error) {
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.mu.Unlock()
		return nil, uerr("Ролик не найден")
	}
	if j.Status != "failed" {
		m.mu.Unlock()
		return nil, uerr("Ролик не в ошибке")
	}
	clips, music, o, by := j.Clips, j.Music, j.Opts, j.By
	m.mu.Unlock()
	nj, err := m.Submit(clips, music, o, by)
	if err == nil {
		m.Delete(id)
	}
	return nj, err
}

func (m *Manager) Job(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[id]; j != nil {
		cp := *j
		return &cp
	}
	return nil
}

// Jobs: newest first.
func (m *Manager) Jobs() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out
}

// Delete removes a job (a running one is stopped first).
func (m *Manager) Delete(id string) {
	m.mu.Lock()
	if c := m.cancel[id]; c != nil {
		c()
	}
	delete(m.jobs, id)
	m.save()
	m.mu.Unlock()
	os.RemoveAll(m.jobDir(id))
}

func (m *Manager) set(id string, f func(j *Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[id]; j != nil {
		was := j.Status
		f(j)
		if j.Status != was {
			m.save()
		}
	}
}

func (m *Manager) run(parent context.Context, id string) {
	m.mu.Lock()
	j := m.jobs[id]
	if j == nil || j.Status != "queued" {
		m.mu.Unlock()
		return
	}
	m.busy++
	if m.busy > m.maxBusy {
		m.maxBusy = m.busy
	}
	j.Status, j.Started, j.Stage, j.Pct = "running", m.Now(), "Запуск", 0
	ctx, cancel := context.WithTimeout(parent, m.Timeout)
	m.cancel[id] = cancel
	in := Input{Music: "", Opts: j.Opts, MaxInput: m.MaxIn}
	for _, c := range j.Clips {
		in.Clips = append(in.Clips, m.upPath(c))
	}
	if j.Music != "" {
		in.Music = m.upPath(j.Music)
	}
	m.save()
	m.mu.Unlock()
	work := filepath.Join(m.Dir, "work", id)
	defer func() {
		cancel()
		os.RemoveAll(work)
		m.mu.Lock()
		m.busy--
		delete(m.cancel, id)
		m.save()
		m.mu.Unlock()
	}()
	t0 := m.Now()
	res, err := m.render(ctx, in, work, func(stage string, done float64) {
		m.set(id, func(j *Job) { j.Stage, j.Pct = stage, int(done*100) })
	})
	if err == nil {
		dir := m.jobDir(id)
		if err = os.MkdirAll(dir, 0o755); err == nil {
			if err = os.Rename(res.Out, m.OutPath(id)); err == nil {
				err = os.Rename(res.Cover, m.CoverPath(id))
			}
		}
	}
	os.RemoveAll(work) // before the status says done
	if err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			msg = fmt.Sprintf("Сборка шла дольше %d минут и остановлена: укоротите клипы", int(m.Timeout.Minutes()))
		case errors.Is(err, context.Canceled):
			msg = "Сборка остановлена"
		}
		log.Printf("video: job %s failed after %s: %v", id, m.Now().Sub(t0).Round(time.Second), err)
		m.set(id, func(j *Job) { j.Status, j.Error, j.Finished, j.Stage = "failed", msg, m.Now(), "" })
		return
	}
	var size int64
	if st, e := os.Stat(m.OutPath(id)); e == nil {
		size = st.Size()
	}
	m.set(id, func(j *Job) {
		j.Dur, j.InDur, j.Cut, j.Words, j.Text, j.Notes, j.Size = res.Dur, res.InDur, res.Cut, res.Words, res.Text, res.Notes, size
	})
	if m.Store != nil {
		cp := m.Job(id)
		sctx, c2 := context.WithTimeout(context.Background(), 2*time.Minute)
		fid, cid := m.Store(sctx, cp, m.OutPath(id), m.CoverPath(id))
		c2()
		m.set(id, func(j *Job) { j.FileID, j.CoverID = fid, cid })
	}
	log.Printf("video: job %s done in %s: %.1f s, %d words, %d MB", id, m.Now().Sub(t0).Round(time.Second), res.Dur, res.Words, size>>20)
	m.set(id, func(j *Job) { j.Status, j.Pct, j.Stage, j.Finished = "done", 100, "", m.Now() })
}

func (m *Manager) render(ctx context.Context, in Input, work string, prog func(string, float64)) (*Result, error) {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	if m.Tools == nil {
		return nil, errors.New("ffmpeg не настроен")
	}
	t, err := m.Tools(ctx)
	if err != nil {
		return nil, err
	}
	r := m.Render
	if r == nil {
		r = Render
	}
	return r(ctx, in, t, work, prog)
}

// Cleanup: uploads older than a day (not in a waiting job), finished jobs
// beyond the last KeepJobs or older than two weeks.
func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()
	inUse := map[string]bool{}
	var fin []*Job
	for _, j := range m.jobs {
		if j.Status == "queued" || j.Status == "running" {
			for _, c := range j.Clips {
				inUse[c] = true
			}
			inUse[j.Music] = true
		} else {
			fin = append(fin, j)
		}
	}
	for id, u := range m.uploads {
		if !inUse[id] && now.Sub(u.Created) > UploadMaxAge {
			delete(m.uploads, id)
			os.Remove(m.upPath(id))
		}
	}
	sort.Slice(fin, func(a, b int) bool { return fin[a].Created.After(fin[b].Created) })
	for i, j := range fin {
		if i >= KeepJobs || now.Sub(j.Created) > JobMaxAge {
			delete(m.jobs, j.ID)
			os.RemoveAll(m.jobDir(j.ID))
		}
	}
	m.capSize(inUse)
	// files nobody knows about (a crash between writes)
	if ents, err := os.ReadDir(filepath.Join(m.Dir, "up")); err == nil {
		for _, e := range ents {
			id := trimExt(e.Name())
			if m.uploads[id] == nil {
				os.Remove(filepath.Join(m.Dir, "up", e.Name()))
			}
		}
	}
	if ents, err := os.ReadDir(filepath.Join(m.Dir, "jobs")); err == nil {
		for _, e := range ents {
			if m.jobs[e.Name()] == nil {
				os.RemoveAll(filepath.Join(m.Dir, "jobs", e.Name()))
			}
		}
	}
	m.save()
}

// dirSize: the bytes under p.
func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Used: the bytes of uploads and finished Reels on disk.
func (m *Manager) Used() int64 {
	return dirSize(filepath.Join(m.Dir, "up")) + dirSize(filepath.Join(m.Dir, "jobs"))
}

// capSize (m.mu held): over MaxBytes, the oldest finished jobs and then the
// oldest free uploads are removed until the folder fits.
func (m *Manager) capSize(inUse map[string]bool) {
	if m.MaxBytes <= 0 {
		return
	}
	used := m.Used()
	if used <= m.MaxBytes {
		return
	}
	type victim struct {
		at   time.Time
		size int64
		drop func()
	}
	var vs []victim
	for _, j := range m.jobs {
		if j.Status == "queued" || j.Status == "running" {
			continue
		}
		j := j
		vs = append(vs, victim{j.Created, dirSize(m.jobDir(j.ID)), func() {
			delete(m.jobs, j.ID)
			os.RemoveAll(m.jobDir(j.ID))
		}})
	}
	var ups []victim
	for id, u := range m.uploads {
		if inUse[id] {
			continue
		}
		id := id
		size := int64(0)
		if st, err := os.Stat(m.upPath(id)); err == nil {
			size = st.Size()
		}
		ups = append(ups, victim{u.Created, size, func() {
			delete(m.uploads, id)
			os.Remove(m.upPath(id))
		}})
	}
	sort.Slice(vs, func(a, b int) bool { return vs[a].at.Before(vs[b].at) })
	sort.Slice(ups, func(a, b int) bool { return ups[a].at.Before(ups[b].at) })
	dropped := 0
	for _, v := range append(vs, ups...) {
		if used <= m.MaxBytes {
			break
		}
		v.drop()
		used -= v.size
		dropped++
	}
	if dropped > 0 {
		log.Printf("video: folder over %d MB: %d oldest files removed, %d MB kept", m.MaxBytes>>20, dropped, used>>20)
	}
}

func trimExt(n string) string {
	if e := filepath.Ext(n); e != "" {
		return n[:len(n)-len(e)]
	}
	return n
}
