package httpapi

import (
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestCanonicalPlatform(t *testing.T) {
	cases := map[string]string{
		"feishu":          "feishu",
		"wechat":          "wechat",
		"wecom":           "wecom",
		"personal_wechat": "wechat",
		"personal-wechat": "wechat",
		"unknown":         "",
	}
	for input, want := range cases {
		if got := canonicalPlatform(input); got != want {
			t.Fatalf("canonicalPlatform(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestRequestedPlatforms(t *testing.T) {
	if got := requestedPlatforms("all"); !reflect.DeepEqual(got, []string{"feishu", "wecom", "wechat"}) {
		t.Fatalf("all platforms mismatch: %#v", got)
	}
	if got := requestedPlatforms("wecom"); !reflect.DeepEqual(got, []string{"wecom"}) {
		t.Fatalf("wecom should stay wecom: %#v", got)
	}
	if got := requestedPlatforms("invalid"); got != nil {
		t.Fatalf("invalid platform should return nil, got %#v", got)
	}
}

func TestMergeSearchItemsPreservesGroupsAndDeduplicates(t *testing.T) {
	rag := []gin.H{
		{"id": "chunk:1", "chunk_id": "c1", "platform": "feishu"},
		{"id": "chunk:1", "chunk_id": "c1", "platform": "feishu"},
		{"chunk_id": "c2", "file_id": "7", "platform": "wechat"},
	}
	conversations := []gin.H{{"id": "conversation:9", "kind": "chat", "platform": "feishu"}}
	qa := []gin.H{{"id": "qa:3", "kind": "qa", "platform": "all"}}
	merged := mergeSearchItems(rag, conversations, qa)
	if len(merged) != 4 {
		t.Fatalf("expected 4 unique results, got %#v", merged)
	}
	if merged[0]["kind"] != "chat" {
		t.Fatalf("conversation results should stay first: %#v", merged)
	}
	if merged[2]["kind"] != "file" {
		t.Fatalf("file result should be classified from file_id: %#v", merged[2])
	}
	if merged[3]["kind"] != "qa" {
		t.Fatalf("qa result should stay last: %#v", merged[3])
	}
}

func TestSearchLogicCases(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 15, 0, 0, time.UTC)
	cases := []struct {
		name string
		run  func(*testing.T)
	}{
		{"canonical_feishu", func(t *testing.T) {
			if got := canonicalPlatform("feishu"); got != "feishu" {
				t.Fatalf("got %q", got)
			}
		}},
		{"canonical_wechat", func(t *testing.T) {
			if got := canonicalPlatform("wechat"); got != "wechat" {
				t.Fatalf("got %q", got)
			}
		}},
		{"canonical_wecom", func(t *testing.T) {
			if got := canonicalPlatform("wecom"); got != "wecom" {
				t.Fatalf("got %q", got)
			}
		}},
		{"canonical_personal_wechat", func(t *testing.T) {
			if got := canonicalPlatform("personal_wechat"); got != "wechat" {
				t.Fatalf("got %q", got)
			}
		}},
		{"canonical_invalid", func(t *testing.T) {
			if got := canonicalPlatform("unknown"); got != "" {
				t.Fatalf("got %q", got)
			}
		}},
		{"requested_all", func(t *testing.T) {
			if got := requestedPlatforms("all"); !reflect.DeepEqual(got, []string{"feishu", "wecom", "wechat"}) {
				t.Fatalf("got %#v", got)
			}
		}},
		{"requested_empty", func(t *testing.T) {
			if got := requestedPlatforms(""); !reflect.DeepEqual(got, []string{"feishu", "wecom", "wechat"}) {
				t.Fatalf("got %#v", got)
			}
		}},
		{"requested_wecom", func(t *testing.T) {
			if got := requestedPlatforms("wecom"); !reflect.DeepEqual(got, []string{"wecom"}) {
				t.Fatalf("got %#v", got)
			}
		}},
		{"requested_invalid", func(t *testing.T) {
			if got := requestedPlatforms("bad"); got != nil {
				t.Fatalf("got %#v", got)
			}
		}},
		{"parse_int64_nil", func(t *testing.T) {
			if got, ok := parseSearchInt64(nil); ok || got != 0 {
				t.Fatalf("got %d %v", got, ok)
			}
		}},
		{"parse_int64_blank", func(t *testing.T) {
			if got, ok := parseSearchInt64("   "); ok || got != 0 {
				t.Fatalf("got %d %v", got, ok)
			}
		}},
		{"parse_int64_value", func(t *testing.T) {
			if got, ok := parseSearchInt64("42"); !ok || got != 42 {
				t.Fatalf("got %d %v", got, ok)
			}
		}},
		{"parse_int64_trim", func(t *testing.T) {
			if got, ok := parseSearchInt64(" 7 "); !ok || got != 7 {
				t.Fatalf("got %d %v", got, ok)
			}
		}},
		{"format_time_nil", func(t *testing.T) {
			if got := formatSearchTime(nil); got != "暂无" {
				t.Fatalf("got %q", got)
			}
		}},
		{"format_time_value", func(t *testing.T) {
			if got := formatSearchTime(&now); got != "2026-08-30 10:15" {
				t.Fatalf("got %q", got)
			}
		}},
		{"search_body_scope", func(t *testing.T) {
			body := searchBody(searchRequest{Query: "项目 A", Limit: 99, StartAt: "2026-08-29T00:00:00Z", EndAt: "2026-08-30T00:00:00Z"}, allowedSearchScope{Platforms: []string{"wechat"}, SourceAccountIDs: []int64{3}, ConversationIDs: []int64{9}})
			if body["limit"] != 10 || body["candidate_top_k"] != 50 {
				t.Fatalf("unexpected body %#v", body)
			}
			if !reflect.DeepEqual(body["platforms"], []string{"wechat"}) {
				t.Fatalf("unexpected body %#v", body)
			}
		}},
		{"search_body_time_filters", func(t *testing.T) {
			body := searchBody(searchRequest{Query: "项目 A", StartAt: "2026-08-29T00:00:00Z", EndAt: "2026-08-30T00:00:00Z"}, allowedSearchScope{Platforms: []string{"feishu"}})
			if body["start_at"] != "2026-08-29T00:00:00Z" || body["end_at"] != "2026-08-30T00:00:00Z" {
				t.Fatalf("unexpected body %#v", body)
			}
		}},
		{"merge_order", func(t *testing.T) {
			merged := mergeSearchItems([]gin.H{{"id": "r1", "kind": "message"}}, []gin.H{{"id": "c1", "kind": "chat"}}, []gin.H{{"id": "q1", "kind": "qa"}})
			if got := []any{merged[0]["kind"], merged[1]["kind"], merged[2]["kind"]}; !reflect.DeepEqual(got, []any{"chat", "message", "qa"}) {
				t.Fatalf("unexpected order %#v", got)
			}
		}},
		{"merge_dedup", func(t *testing.T) {
			merged := mergeSearchItems([]gin.H{{"id": "dup", "kind": "message"}, {"id": "dup", "kind": "message"}}, nil, nil)
			if len(merged) != 1 {
				t.Fatalf("unexpected merged %#v", merged)
			}
		}},
		{"merge_file_kind", func(t *testing.T) {
			merged := mergeSearchItems([]gin.H{{"chunk_id": "x", "file_id": "9"}}, nil, nil)
			if merged[0]["kind"] != "file" {
				t.Fatalf("unexpected merged %#v", merged)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, tc.run)
	}
}
