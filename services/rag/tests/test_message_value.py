from __future__ import annotations

import json
import unittest

from app.services.message_value import MessageValueEvaluator


class MessageValueEvaluatorTests(unittest.TestCase):
    def evaluator(self, response: str) -> MessageValueEvaluator:
        evaluator = MessageValueEvaluator(
            base_url="http://test.invalid/v1",
            api_key="test-key",
            enabled=True,
        )
        evaluator._call_model = lambda payload: {
            "choices": [{"message": {"content": response}}]
        }
        return evaluator

    def test_parses_valuable_json(self):
        decision = self.evaluator(
            '```json\n{"valuable":true,"confidence":0.9,"categories":["task"],"reason":"有明确任务"}\n```'
        ).evaluate("feishu", {"chat_id": "oc_group", "text": "请周五前完成"})
        self.assertTrue(decision.valuable)
        self.assertTrue(decision.evaluated)
        self.assertEqual(decision.categories, ["task"])

    def test_parses_low_value_json(self):
        decision = self.evaluator(
            json.dumps({"valuable": False, "confidence": 0.99, "categories": ["noise"]})
        ).evaluate("wechat", {"chat_id": "room@chatroom", "content": "好的"})
        self.assertFalse(decision.valuable)
        self.assertTrue(decision.evaluated)

    def test_invalid_boolean_fails_open(self):
        decision = self.evaluator('{"valuable":"unknown"}').evaluate(
            "feishu", {"chat_id": "oc_group", "text": "消息"}
        )
        self.assertTrue(decision.valuable)
        self.assertFalse(decision.evaluated)

    def test_unconfigured_evaluator_fails_open(self):
        decision = MessageValueEvaluator(enabled=True, base_url="", api_key="").evaluate(
            "feishu", {"chat_id": "oc_group", "text": "消息"}
        )
        self.assertTrue(decision.valuable)
        self.assertFalse(decision.evaluated)


if __name__ == "__main__":
    unittest.main()
