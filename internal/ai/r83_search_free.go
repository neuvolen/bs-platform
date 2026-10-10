package ai

import "context"

// SearchFree (R83, «ИИ-видимость» in Маркетинг → SEO): the question goes to
// the free model with web search only, Gemini with Google Search grounding,
// as a person would ask it. No paid model is used: with no Gemini key, its
// grounding refused or the free budget used up, the error says so
// (SearchUnavailable(err) is true) and the caller writes «нет данных».
// The model's name comes back for the history.
func (c *Client) SearchFree(ctx context.Context, prompt string) (string, string, error) {
	if c == nil || c.Gemini == "" || GeminiDisabled() {
		return "", "", ErrNoSearch
	}
	if q := c.geminiSearchClosed(); q != nil {
		return "", "", q
	}
	if q := c.quotaClosed("gemini"); q != nil {
		return "", "", q
	}
	if ke := c.keyClosed("gemini"); ke != nil {
		return "", "", ke
	}
	if err := c.spend("gemini_search"); err != nil {
		return "", "", err
	}
	if err := c.spend("gemini"); err != nil {
		return "", "", err
	}
	ans, err := c.geminiSearch(ctx, prompt)
	c.noteGeminiSearch(err)
	return ans, c.geminiModel(), err
}
