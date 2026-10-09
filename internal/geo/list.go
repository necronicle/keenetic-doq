// Package geo — гео-полоса doqd: геоблокированные имена резолвятся только
// закреплённым сервером обхода геоблокировок, остальные — самым быстрым.
package geo

import "strings"

// builtinDomains — заведомо геоблокированные сервисы. Совпадение по суффиксу
// по границе метки: openai.com покрывает auth.openai.com.
var builtinDomains = []string{
	"openai.com", "chatgpt.com", "oaistatic.com", "oaiusercontent.com", "sora.com",
	"anthropic.com", "claude.ai", "claude.com", "claudeusercontent.com",
	"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com",
	"notebooklm.google.com", "x.ai", "grok.com",
}

// ProbeDomains — хосты, нужные рабочему сеансу сервиса: по ним оцениваются
// серверы обхода (охват = сколько из них сервер подменяет живым прокси) и
// пополняется отпечаток пула закреплённого. Сервер, подменяющий только сам
// сайт (chatgpt.com), но отдающий настоящие адреса sentinel/tcr9i, сервис не
// открывает: антибот и капча идут с российского адреса и отклоняются.
// Меняя список, меняют и набор в geo.state: при расхождении выбор переоценивается.
var ProbeDomains = []string{
	"chatgpt.com.", "sentinel.openai.com.", "tcr9i.chat.openai.com.", "auth.openai.com.",
	"claude.ai.", "assets-proxy.anthropic.com.", "gemini.google.com.",
}

// BuiltinDomains отдаёт копию встроенного списка.
func BuiltinDomains() []string { return append([]string(nil), builtinDomains...) }

// normalize: нижний регистр, без завершающей точки.
func normalize(name string) string { return strings.TrimSuffix(strings.ToLower(name), ".") }

// Matcher — множество доменов с совпадением по суффиксу.
type Matcher struct{ set map[string]struct{} }

func NewMatcher(lists ...[]string) *Matcher {
	m := &Matcher{set: map[string]struct{}{}}
	for _, l := range lists {
		for _, d := range l {
			if d = normalize(d); d != "" {
				m.set[d] = struct{}{}
			}
		}
	}
	return m
}

// Match: имя совпадает с доменом из множества или является его поддоменом.
func (m *Matcher) Match(name string) bool {
	n := normalize(name)
	for n != "" {
		if _, ok := m.set[n]; ok {
			return true
		}
		i := strings.IndexByte(n, '.')
		if i < 0 {
			return false
		}
		n = n[i+1:]
	}
	return false
}
