#!/usr/bin/env python3
"""Evaluate retrieval outputs against the hybrid-retrieval-plan dataset.

Input JSONL records contain ``expected_files``/``expected_documents`` and a
``results`` array. Each result may provide ``file_name``, ``document_id``,
``chunk_id`` or ``source_file``. The script deliberately evaluates recall
only; answer quality and citation permission checks remain separate gates.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any


def _match(record: dict[str, Any], result: dict[str, Any]) -> bool:
    expected_files = {str(value) for value in record.get("expected_files", [])}
    expected_docs = {str(value) for value in record.get("expected_documents", [])}
    file_value = str(result.get("file_name") or result.get("source_file") or "")
    doc_value = str(result.get("document_id") or "")
    chunk_value = str(result.get("chunk_id") or "")
    expected_chunks = {str(value) for value in record.get("expected_chunks", [])}
    return ((bool(expected_files) and file_value in expected_files)
            or (bool(expected_docs) and doc_value in expected_docs)
            or (bool(expected_chunks) and chunk_value in expected_chunks))


def _permission_violation(record: dict[str, Any], result: dict[str, Any]) -> bool:
    """Return true when a result is outside the scope recorded for the query."""
    user = record.get("user_id")
    if user is not None and str(result.get("user_id")) != str(user):
        return True
    accounts = {str(value) for value in record.get("allowed_source_account_ids", [])}
    if accounts and str(result.get("source_account_id")) not in accounts:
        return True
    conversations = {str(value) for value in record.get("allowed_conversation_ids", [])}
    if conversations and str(result.get("conversation_id")) not in conversations:
        return True
    return False


def evaluate(records: list[dict[str, Any]], ks: tuple[int, ...] = (5, 10, 20)) -> dict[str, Any]:
    metrics = {f"recall_at_{k}": 0.0 for k in ks}
    evaluated = 0
    permission_checks = 0
    permission_errors = 0
    for record in records:
        # no-answer cases are excluded from recall denominator by design
        expected = record.get("expected_files") or record.get("expected_documents") or record.get("expected_chunks")
        if not expected:
            continue
        evaluated += 1
        results = record.get("results") or []
        for result in results:
            if record.get("user_id") is not None or record.get("allowed_source_account_ids") or record.get("allowed_conversation_ids"):
                permission_checks += 1
                permission_errors += int(_permission_violation(record, result))
        for k in ks:
            if any(_match(record, result) for result in results[:k]):
                metrics[f"recall_at_{k}"] += 1
    if evaluated:
        for key in metrics:
            metrics[key] = round(metrics[key] / evaluated, 4)
    metrics["evaluated"] = evaluated
    metrics["total"] = len(records)
    metrics["permission_checks"] = permission_checks
    metrics["permission_errors"] = permission_errors
    metrics["permission_error_rate"] = round(permission_errors / permission_checks, 4) if permission_checks else 0.0
    metrics["pass_permissions"] = permission_errors == 0
    metrics["pass_90"] = evaluated == 0 or metrics["recall_at_10"] >= 0.90
    metrics["pass_85_fallback"] = evaluated == 0 or metrics["recall_at_10"] >= 0.85
    metrics["threshold_used"] = "none" if evaluated == 0 else ("90%" if metrics["pass_90"] else ("85%_fallback" if metrics["pass_85_fallback"] else "not_met"))
    return metrics


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("input", type=Path, help="retrieval result JSONL")
    args = parser.parse_args()
    records = [json.loads(line) for line in args.input.read_text(encoding="utf-8").splitlines() if line.strip()]
    metrics = evaluate(records)
    print(json.dumps(metrics, ensure_ascii=False, indent=2))
    return 0 if metrics["pass_90"] or metrics["pass_85_fallback"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
