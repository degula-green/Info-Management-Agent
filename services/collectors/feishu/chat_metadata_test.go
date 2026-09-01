package main

import "testing"

func TestChatHasMemberCount(t *testing.T) {
	tests := []struct {
		name string
		chat map[string]any
		want bool
	}{
		{name: "user count", chat: map[string]any{"user_count": float64(29)}, want: true},
		{name: "string count", chat: map[string]any{"member_count": "29"}, want: true},
		{name: "member array", chat: map[string]any{"members": []any{"a", "b"}}, want: true},
		{name: "missing", chat: map[string]any{"chat_id": "oc_example", "name": "degula, DC, DC"}, want: false},
		{name: "zero", chat: map[string]any{"user_count": float64(0)}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := chatHasMemberCount(tt.chat); got != tt.want {
				t.Fatalf("chatHasMemberCount() = %v, want %v", got, tt.want)
			}
		})
	}
}
