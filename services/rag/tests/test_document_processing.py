from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from app.services.document_parser import parse_local
from app.services.vectorization import document_chunks


class LocalDocumentParserTests(unittest.TestCase):
    def test_markdown_is_preserved(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "notes.md"
            path.write_text("# Title\n\nBody", encoding="utf-8")
            content, parser = parse_local(path, "md")
        self.assertEqual(parser, "local-text")
        self.assertEqual(content, "# Title\n\nBody")

    def test_csv_becomes_readable_text(self) -> None:
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "report.csv"
            path.write_text("name,count\na,2\n", encoding="utf-8")
            content, parser = parse_local(path, "csv")
        self.assertEqual(parser, "local-csv")
        self.assertIn("name | count", content)
        self.assertIn("a | 2", content)


class DocumentChunkTests(unittest.TestCase):
    def test_chunk_id_is_stable_and_keeps_heading(self) -> None:
        document = {"document_id": 17, "attachment_id": 8, "message_id": "m-1", "content": "# Agenda\nDiscuss roadmap.",
                    "source": "feishu", "source_account_id": 3, "user_id": 1, "metadata": {}}
        first, second = document_chunks(document), document_chunks(document)
        self.assertEqual([item["chunk_id"] for item in first], [item["chunk_id"] for item in second])
        self.assertEqual(first[0]["metadata"]["heading_path"], ["Agenda"])
        self.assertEqual(first[0]["file_id"], "8")
        self.assertEqual(first[0]["attachment_id"], 8)
        self.assertEqual(first[0]["document_title"], "Agenda")


if __name__ == "__main__":
    unittest.main()  # pragma: no cover

