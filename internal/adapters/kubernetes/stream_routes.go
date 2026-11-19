package kubernetes

import (
	"maps"
	"strings"
)

func pathMatch(kind, path string) map[string]string {
	return map[string]string{"type": kind, "value": path}
}

func streamHeaderRule(rule map[string]any, service string, path map[string]string) map[string]any {
	if path == nil {
		path = pathMatch("PathPrefix", "/")
	}
	websocket := map[string]string{"name": "upgrade", "type": "RegularExpression", "value": "(?i)^websocket$"}
	sse := map[string]string{"name": "accept", "type": "RegularExpression", "value": `(?i)^([ \t]*[^,]+,)*[ \t]*text/event-stream[ \t]*(;[^,]*)?(,.*)?$`}
	entry := streamRouteRule(rule, service, path, websocket)
	entry["matches"] = append(entry["matches"].([]any), map[string]any{"path": path, "headers": []any{sse}})
	return entry
}

func streamRouteRule(rule map[string]any, service string, path, header map[string]string) map[string]any {
	entry := maps.Clone(rule)
	match := map[string]any{}
	if path != nil {
		match["path"] = path
	}
	if header != nil {
		match["headers"] = []any{header}
	}
	entry["matches"] = []any{match}
	entry["backendRefs"] = []any{map[string]any{"name": service, "port": 8081}}
	entry["timeouts"] = map[string]string{"request": "0s", "backendRequest": "0s"}
	return entry
}

// Match the gateway's path precedence before generating a new exact path.
// This preserves more specific origin and disabled routes.
func winningHostnameRule(path string, rules []HostnameRule) int {
	best := -1
	for index, rule := range rules {
		if rule.Match == "exact" && rule.Path == path {
			return index
		}
		if rule.Match == "prefix" && (rule.Path == "/" || path == rule.Path || strings.HasPrefix(path, strings.TrimSuffix(rule.Path, "/")+"/")) &&
			(best < 0 || len(rule.Path) > len(rules[best].Path)) {
			best = index
		}
	}
	return best
}
