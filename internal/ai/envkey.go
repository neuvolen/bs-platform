package ai

import (
	"os"
	"sort"
	"strings"
)

// R37: the Claude key in Railway is found however the variable is named or
// pasted. The usual names come first (ANTHROPIC_API_KEY, CLAUDE_API_KEY);
// then any variable whose name has ANTHROPIC or CLAUDE together with KEY or
// TOKEN (any case, spaces around); then any variable whose value is an
// Anthropic key (sk-ant-…). The value is cleaned of quotes, spaces, line
// breaks, a «Bearer » prefix and a pasted «NAME=» in front.
//
// Only variable NAMES ever leave this file (the system check, the
// settings); a value never does.

// EnvKeyInfo: what the environment gives for the Claude key.
type EnvKeyInfo struct {
	Key     string   // the cleaned key; never printed
	Name    string   // the variable it came from, "" when none
	Cleaned bool     // quotes, spaces, «Bearer » or «NAME=» were removed
	Related []string // names that look related (the used one included)
	// Unusable: related names whose value is not a usable key, with the reason in Russian.
	Unusable map[string]string
}

var keyEnvNames = []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"}

// envNameLooksLikeKey: ANTHROPIC/CLAUDE with KEY/TOKEN, or just ANTHROPIC/CLAUDE.
func envNameLooksLikeKey(name string) bool {
	n := strings.ToUpper(strings.TrimSpace(name))
	n = strings.NewReplacer(" ", "_", "-", "_", ".", "_").Replace(n)
	if !strings.Contains(n, "ANTHROPIC") && !strings.Contains(n, "CLAUDE") {
		return false
	}
	switch n {
	case "ANTHROPIC", "CLAUDE":
		return true
	}
	return strings.Contains(n, "KEY") || strings.Contains(n, "TOKEN")
}

// CleanKey: the key as pasted, without quotes, spaces, line breaks, a
// «Bearer »/«x-api-key:» prefix or a «NAME=» in front. A value that has an
// sk-ant- key inside gives that key.
func CleanKey(v string) (key string, cleaned bool) {
	orig := v
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "sk-ant-"); i > 0 {
		v = v[i:]
	}
	for {
		before := v
		v = strings.TrimSpace(v)
		v = strings.Trim(v, "\"'`«»“”„")
		low := strings.ToLower(v)
		for _, p := range []string{"bearer ", "bearer:", "x-api-key:", "x-api-key="} {
			if strings.HasPrefix(low, p) {
				v = v[len(p):]
				low = strings.ToLower(v)
			}
		}
		if v == before {
			break
		}
	}
	// a key ends at the first space, quote or line break
	if i := strings.IndexFunc(v, func(r rune) bool {
		return r <= ' ' || r == '"' || r == '\'' || r == '`' || r == ';' || r == ','
	}); i > 0 && strings.HasPrefix(v, "sk-ant-") {
		v = v[:i]
	}
	return v, v != orig
}

// isAdminKey: an Admin API key (sk-ant-admin…) cannot call the models.
func isAdminKey(v string) bool { return strings.HasPrefix(v, "sk-ant-admin") }

// EnvKey scans the environment for the Claude key.
func EnvKey() EnvKeyInfo { return envKeyFrom(os.Environ()) }

func envKeyFrom(environ []string) EnvKeyInfo {
	out := EnvKeyInfo{Unusable: map[string]string{}}
	vals := map[string]string{}
	var names []string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := vals[k]; !seen {
			names = append(names, k)
		}
		vals[k] = v
	}
	sort.Strings(names)
	try := func(name string) bool {
		key, cleaned := CleanKey(vals[name])
		switch {
		case key == "":
			out.Unusable[name] = "пустое значение"
			return false
		case isAdminKey(key):
			out.Unusable[name] = "это ключ администратора (sk-ant-admin…), нужен обычный API-ключ"
			return false
		case !ValidKeyShape(key):
			out.Unusable[name] = "значение не похоже на ключ (нужно sk-ant-…, одной строкой)"
			return false
		}
		out.Key, out.Name, out.Cleaned = key, name, cleaned
		delete(out.Unusable, name)
		return true
	}
	related := map[string]bool{}
	for _, n := range names {
		if envNameLooksLikeKey(n) {
			related[n] = true
		} else if k, _ := CleanKey(vals[n]); strings.HasPrefix(k, "sk-ant-") {
			related[n] = true
		}
	}
	for n := range related {
		out.Related = append(out.Related, n)
	}
	sort.Strings(out.Related)
	// 1. the usual names, exactly
	for _, n := range keyEnvNames {
		if _, ok := vals[n]; ok && try(n) {
			return out
		}
	}
	// 2. names that look like the key (exact usual names already tried)
	for _, n := range names {
		if envNameLooksLikeKey(n) && n != keyEnvNames[0] && n != keyEnvNames[1] && try(n) {
			return out
		}
	}
	// 3. any variable holding an sk-ant- key
	for _, n := range names {
		if envNameLooksLikeKey(n) {
			continue
		}
		if k, _ := CleanKey(vals[n]); strings.HasPrefix(k, "sk-ant-") && try(n) {
			return out
		}
	}
	return out
}
