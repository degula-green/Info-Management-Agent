package main

import (
	"regexp"
	"strings"
)

var reactionOnlyPattern = regexp.MustCompile(`^(\[[^\[\]<>\r\n]{1,16}\]|[\x{1F300}-\x{1FAFF}\x{2600}-\x{27BF}])+[\x{FE0E}\x{FE0F}\x{200D}]*$`)

func obviousMessageNoise(raw map[string]any) bool {
	typ, _ := raw["msg_type"].(string)
	typ = strings.ToLower(strings.TrimSpace(typ))
	if typ != "" && typ != "text" && typ != "txt" && typ != "1" {
		return false
	}
	content, _ := raw["content"].(string)
	if content == "" {
		if body, ok := raw["body"].(map[string]any); ok {
			content, _ = body["content"].(string)
		}
	}
	return reactionOnlyPattern.MatchString(strings.TrimSpace(content))
}
