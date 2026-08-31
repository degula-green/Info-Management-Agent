package main

import "testing"

func TestObviousMessageNoise(t *testing.T) {
	if !obviousMessageNoise(map[string]any{"msg_type": "text", "content": "[耶]"}) {
		t.Fatal("reaction should be filtered")
	}
	if obviousMessageNoise(map[string]any{"msg_type": "text", "content": "需要在周五前完成[耶]接口"}) {
		t.Fatal("business text must be retained")
	}
	if obviousMessageNoise(map[string]any{"msg_type": "file", "content": "[耶]"}) {
		t.Fatal("non-text messages must be retained")
	}
}
