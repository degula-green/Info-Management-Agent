from __future__ import annotations

import json
import os
import unittest
from unittest.mock import patch

from services.collectors.value_judgment import MessageValueClient


class MessageValueClientTests(unittest.TestCase):
    def test_invalid_timeout_uses_default(self):
        with patch.dict(os.environ, {"MESSAGE_VALUE_TIMEOUT_SECONDS": "invalid"}):
            self.assertEqual(MessageValueClient(endpoint="").timeout, 10.0)

    def test_direct_message_does_not_call_endpoint(self):
        client = MessageValueClient(endpoint="http://unused", timeout=0.1)
        with patch("services.collectors.value_judgment.urllib.request.urlopen") as urlopen:
            self.assertTrue(client.is_valuable("feishu", {"chat_id": "ou_direct"}))
            urlopen.assert_not_called()

    def test_feishu_p2p_message_does_not_call_endpoint(self):
        client = MessageValueClient(endpoint="http://unused", timeout=0.1)
        with patch("services.collectors.value_judgment.urllib.request.urlopen") as urlopen:
            self.assertTrue(client.is_valuable("feishu", {"chat_id": "oc_p2p", "chat_type": "p2p"}))
            urlopen.assert_not_called()

    def test_group_message_uses_boolean_result(self):
        client = MessageValueClient(endpoint="http://value", timeout=1)

        class Response:
            status = 200

            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self): return json.dumps({"valuable": False}).encode()

        with patch("services.collectors.value_judgment.urllib.request.urlopen", return_value=Response()) as urlopen:
            self.assertFalse(client.is_valuable("wechat", {"chat_id": "room@chatroom", "content": "好的"}))
            urlopen.assert_called_once()

    def test_endpoint_failure_fails_open(self):
        client = MessageValueClient(endpoint="http://value", timeout=1)
        with patch(
            "services.collectors.value_judgment.urllib.request.urlopen",
            side_effect=OSError("offline"),
        ):
            self.assertTrue(client.is_valuable("feishu", {"chat_id": "oc_group"}))


if __name__ == "__main__":
    unittest.main()
