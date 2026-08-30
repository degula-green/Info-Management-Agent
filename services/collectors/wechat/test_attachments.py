import unittest

from services.collectors.wechat.attachments import WeChatAttachmentParser


class FakeDB:
    def get_message_row(self, user, local_id):
        return {"server_id": None, "local_type": 49}


class AttachmentParserTests(unittest.TestCase):
    def test_file_type_49_extracts_supported_name_and_stable_id(self):
        parser = WeChatAttachmentParser(FakeDB())
        values = parser.parse("wxid_a", "wxid_b", {"local_type": 49, "local_id": 7, "content": "report.pdf"})
        self.assertEqual(values[0]["external_attachment_id"], "wxid_a:wxid_b:7:0")
        self.assertEqual(values[0]["extension"], "pdf")
        self.assertEqual(values[0]["file_category"], "document")

    def test_unsupported_file_is_metadata_only(self):
        parser = WeChatAttachmentParser(FakeDB())
        values = parser.parse("wxid_a", "chat", {"type": 49, "local_id": 8, "file_name": "photo.jpg"})
        self.assertTrue(values[0]["unsupported"])
        self.assertEqual(values[0]["file_category"], "unknown")

    def test_missing_name_is_recorded_as_parse_error(self):
        parser = WeChatAttachmentParser(FakeDB())
        values = parser.parse("wxid_a", "chat", {"type": 49, "local_id": 9})
        self.assertEqual(values[0]["parse_error"], "WeChat file name could not be resolved")


if __name__ == "__main__":
    unittest.main()
