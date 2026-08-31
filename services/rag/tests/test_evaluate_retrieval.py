import unittest

from scripts.evaluate_retrieval import evaluate


class RetrievalEvaluationTests(unittest.TestCase):
    def test_file_and_document_matches_count_as_hits(self):
        records = [
            {"expected_files": ["a.docx"], "results": [{"file_name": "a.docx"}]},
            {"expected_documents": ["9"], "results": [{"document_id": 9}]},
            {"expected_chunks": ["c"], "results": [{"chunk_id": "c"}]},
        ]
        metrics = evaluate(records)
        self.assertEqual(metrics["recall_at_10"], 1.0)
        self.assertTrue(metrics["pass_90"])

    def test_no_answer_case_is_not_recall_failure(self):
        metrics = evaluate([{"id": "no-answer", "results": []}])
        self.assertEqual(metrics["evaluated"], 0)
        self.assertTrue(metrics["pass_90"])

    def test_permission_errors_are_reported(self):
        metrics = evaluate([{"user_id": 7, "allowed_conversation_ids": [3],
                             "expected_files": ["a.docx"],
                             "results": [{"user_id": 8, "conversation_id": 99, "file_name": "a.docx"}]}])
        self.assertEqual(metrics["permission_errors"], 1)
        self.assertFalse(metrics["pass_permissions"])

    def test_threshold_reports_fallback_when_recall_is_between_85_and_90(self):
        records = []
        for index in range(20):
            records.append({"expected_files": ["a.docx"], "results": ([{"file_name": "a.docx"}] if index < 17 else [])})
        metrics = evaluate(records)
        self.assertEqual(metrics["recall_at_10"], 0.85)
        self.assertEqual(metrics["threshold_used"], "85%_fallback")


if __name__ == "__main__":
    unittest.main()
