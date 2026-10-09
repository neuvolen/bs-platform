package ai

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// R70: the pauses survive a deploy. The server is deployed 10-20 times a day
// and each start forgot them: Claude with no balance was asked again (and
// paused again) on every start, the morning events search ran into the same
// refusals, and the start's Gemini self-test spent one of the main model's 20
// free requests a day each time, so the daily limit ran out by noon. Now the
// pauses and the last self-test are kept in a small file on the volume and
// read back at the start.

type quotaFile struct {
	Until    map[string]time.Time `json:"until"`
	Daily    map[string]bool      `json:"daily,omitempty"`
	Billing  map[string]bool      `json:"billing,omitempty"`
	SelfTest time.Time            `json:"selftest,omitempty"`
}

// PersistQuota keeps the quota pauses in path (on the volume) and loads the
// ones still running. "" turns it off.
func (c *Client) PersistQuota(path string) {
	if c == nil || path == "" {
		return
	}
	c.quota.mu.Lock()
	defer c.quota.mu.Unlock()
	c.quota.file = path
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var f quotaFile
	if json.Unmarshal(b, &f) != nil {
		return
	}
	if c.quota.until == nil {
		c.quota.until, c.quota.daily = map[string]time.Time{}, map[string]bool{}
	}
	if c.quota.billing == nil {
		c.quota.billing = map[string]bool{}
	}
	now := quotaNow()
	n := 0
	for svc, u := range f.Until {
		if u.After(now) && u.After(c.quota.until[svc]) {
			c.quota.until[svc], c.quota.daily[svc], c.quota.billing[svc] = u, f.Daily[svc], f.Billing[svc]
			n++
		}
	}
	c.quota.selftest = f.SelfTest
	if n > 0 {
		log.Printf("ai: %d quota pause(s) kept from before the restart", n)
	}
}

// saveQuotaLocked writes the pauses still running; c.quota.mu is held.
func (c *Client) saveQuotaLocked() {
	if c.quota.file == "" {
		return
	}
	now := quotaNow()
	f := quotaFile{Until: map[string]time.Time{}, Daily: map[string]bool{}, Billing: map[string]bool{}, SelfTest: c.quota.selftest}
	for svc, u := range c.quota.until {
		if u.After(now) {
			f.Until[svc], f.Daily[svc], f.Billing[svc] = u, c.quota.daily[svc], c.quota.billing[svc]
		}
	}
	b, _ := json.Marshal(f)
	tmp := c.quota.file + ".tmp"
	if err := os.MkdirAll(filepath.Dir(c.quota.file), 0o755); err != nil {
		return
	}
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.quota.file)
	}
}

// selfTestDue: the Gemini self-test has not run in the last every (and
// marks it as run now when it is due).
func (c *Client) selfTestDue(every time.Duration) bool {
	c.quota.mu.Lock()
	defer c.quota.mu.Unlock()
	now := quotaNow()
	if !c.quota.selftest.IsZero() && now.Sub(c.quota.selftest) < every {
		return false
	}
	c.quota.selftest = now
	c.saveQuotaLocked()
	return true
}
