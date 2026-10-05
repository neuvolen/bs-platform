package ai

import (
	"errors"
	"strconv"
	"strings"
)

// ClaudeKeyHint: what is wrong with the shape of the Claude key in use, in
// Russian, without revealing it (length, start, end only). "" when it looks fine.
func (c *Client) ClaudeKeyHint() string {
	if c == nil {
		return ""
	}
	return claudeShapeHint(c.claudeKey())
}

func claudeShapeHint(k string) string {
	if k == "" {
		return ""
	}
	n := len(k)
	switch {
	case !strings.HasPrefix(k, "sk-ant-"):
		return "ключ должен начинаться с sk-ant-, а начинается иначе (" + strconv.Itoa(n) + " символов)"
	case strings.HasPrefix(k, "sk-ant-api03-") && (n < 100 || !strings.HasSuffix(k, "AA")):
		return "ключ скопирован не полностью: в нём " + strconv.Itoa(n) + " символов, а полный ключ Anthropic около 108 символов и заканчивается на AA"
	}
	return ""
}

// ElevenKeyHint: the same for an ElevenLabs key (sk_ and 48 characters).
func ElevenKeyHint(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	n := len(k)
	switch {
	case !strings.HasPrefix(k, "sk_"):
		return "ключ ElevenLabs должен начинаться с sk_, а начинается иначе (" + strconv.Itoa(n) + " символов); возможно, вставлен id голоса или другой код"
	case n != 51:
		return "ключ ElevenLabs обычно 51 символ (sk_ и 48 знаков), а в этом " + strconv.Itoa(n) + ": похоже, скопирован не полностью"
	}
	return ""
}

// HTTPDetail: the provider's status code and short reason for the system check (no keys inside).
func HTTPDetail(err error) string {
	var he *HTTPError
	if errors.As(err, &he) {
		return "ответ Anthropic " + strconv.Itoa(he.Status) + shortReason(he.Body)
	}
	var ee *ElevenError
	if errors.As(err, &ee) {
		s := "ответ ElevenLabs " + strconv.Itoa(ee.Status)
		if ee.Code != "" {
			s += " " + ee.Code
		}
		return s
	}
	return ""
}

func shortReason(body string) string {
	low := strings.ToLower(body)
	for _, t := range []string{"authentication_error", "permission_error", "invalid x-api-key", "not_found_error", "billing", "credit balance"} {
		if strings.Contains(low, t) {
			return " (" + t + ")"
		}
	}
	return ""
}
