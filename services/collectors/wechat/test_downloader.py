import hashlib
import tempfile
import unittest
from pathlib import Path

from services.collectors.wechat.downloader import WeChatAttachmentDownloadWorker


class FakeRepo:
    def __init__(self):
        self.completed = []
        self.failed = []

    def get_attachment_context(self, attachment_id):
        return {
            "file_name": "report.pdf", "extension": "pdf", "mime_type": "application/pdf",
            "source_message_id": "chat:7", "chat_id": "chat", "local_id": 7,
            "message_id": "message", "conversation_id": "conversation", "user_id": "1",
            "occurred_at": "2026-08-30T10:00:00+00:00",
        }

    def complete_attachment_download(self, *args):
        self.completed.append(args)

    def fail_attachment_download(self, *args):
        self.failed.append(args)

    def dead_attachment_download_ids(self):
        return set()


class FakeMedia:
    def download_file(self, chat_id, local_id, save_dir):
        path = Path(save_dir) / "source.pdf"
        path.write_bytes(b"pdf-body")
        return str(path)


class FakeMinio:
    def __init__(self, fail=False):
        self.fail = fail
        self.objects = []

    def bucket_exists(self, bucket):
        return True

    def make_bucket(self, bucket):
        pass

    def fput_object(self, bucket, key, path, content_type=None):
        if self.fail:
            raise OSError("minio unavailable")
        self.objects.append((bucket, key, Path(path).read_bytes(), content_type))


class DownloadWorkerTests(unittest.TestCase):
    def test_success_uploads_hash_and_removes_temp_directory(self):
        with tempfile.TemporaryDirectory() as root:
            repo, client = FakeRepo(), FakeMinio()
            worker = WeChatAttachmentDownloadWorker(repo, object(), {"wxid": "wxid_a"}, root, client=client, media_downloader=FakeMedia())
            worker.process({"task_id": 3, "payload": {"attachment_id": 11}})
            self.assertEqual(repo.failed, [])
            self.assertEqual(repo.completed[0][0:3], (11, 3, 8))
            self.assertEqual(repo.completed[0][3], hashlib.sha256(b"pdf-body").hexdigest())
            self.assertFalse(worker.root.joinpath("11").exists())
            self.assertTrue(client.objects[0][1].endswith("/wechat/conversation/message/11-report.pdf"))

    def test_upload_failure_keeps_retryable_temp_file(self):
        with tempfile.TemporaryDirectory() as root:
            repo = FakeRepo()
            worker = WeChatAttachmentDownloadWorker(repo, object(), {"wxid": "wxid_a"}, root, client=FakeMinio(fail=True), media_downloader=FakeMedia())
            worker.process({"task_id": 4, "payload": {"attachment_id": 12}})
            self.assertEqual(repo.completed, [])
            self.assertEqual(repo.failed[0][0:2], (12, 4))
            self.assertTrue(worker.root.joinpath("12", "payload.bin").exists())
            self.assertTrue(worker.root.joinpath("12", "state.json").exists())


if __name__ == "__main__":
    unittest.main()
