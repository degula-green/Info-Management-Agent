from __future__ import annotations

import csv
import json
import os
import shutil
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from app.config import settings
from app.services.object_store import ObjectStore, derived_prefix


SUPPORTED_MINERU = {"pdf", "docx", "pptx", "xlsx"}
SUPPORTED_LOCAL = {"txt", "md", "csv"}


@dataclass
class CanonicalDocument:
    content: str
    markdown: str
    headings: list[str]
    parser: str
    parser_version: str
    derived_markdown_key: str
    canonical_key: str
    asset_keys: list[str]


def _clean(value: str) -> str:
    return "\n".join(line.rstrip() for line in value.replace("\r\n", "\n").replace("\r", "\n").split("\n")).strip()


def _headings(markdown: str) -> list[str]:
    return [line.lstrip("# ").strip() for line in markdown.splitlines() if line.startswith("#") and line.lstrip("# ").strip()]


def parse_local(path: Path, extension: str) -> tuple[str, str]:
    if extension in {"txt", "md"}:
        text = path.read_text(encoding="utf-8-sig", errors="replace")
        markdown = text if extension == "md" else text
        return _clean(markdown), "local-text"
    with path.open("r", encoding="utf-8-sig", errors="replace", newline="") as stream:
        rows = list(csv.reader(stream))
    markdown_rows = []
    for row in rows:
        markdown_rows.append(" | ".join(cell.replace("\n", " ").strip() for cell in row))
    return _clean("\n".join(markdown_rows)), "local-csv"


def parse_mineru(path: Path, output_dir: Path) -> tuple[str, str]:
    """Run the documented local MinerU entry point, imported only for parse tasks."""
    try:
        from mineru.cli.common import do_parse, read_fn
    except ModuleNotFoundError as exc:
        raise RuntimeError("MinerU is not installed; install services/rag/requirements.txt and pre-download its models") from exc
    if settings.mineru_model_dir:
        os.environ.setdefault("MINERU_MODEL_SOURCE", settings.mineru_model_dir)
    os.environ.setdefault("MINERU_DEVICE", settings.mineru_device)
    do_parse(str(output_dir), [path.stem], [read_fn(path)], ["auto"], backend="pipeline",
             f_draw_layout_bbox=False, f_draw_span_bbox=False, f_dump_orig_pdf=False,
             f_dump_model_output=False, f_dump_middle_json=False, f_dump_content_list=True)
    markdown_files = sorted(output_dir.rglob("*.md"))
    if not markdown_files:
        raise RuntimeError("MinerU completed without Markdown output")
    return _clean(markdown_files[0].read_text(encoding="utf-8", errors="replace")), "mineru"


class AttachmentDocumentParser:
    def __init__(self, store: ObjectStore | None = None):
        self.store = store or ObjectStore()

    def parse(self, context: dict[str, Any]) -> CanonicalDocument:
        extension = str(context.get("extension") or "").lower().lstrip(".")
        if extension not in SUPPORTED_MINERU | SUPPORTED_LOCAL:
            raise ValueError(f"unsupported attachment extension: {extension or 'unknown'}")
        workdir = Path(tempfile.mkdtemp(prefix=f"info-agent-attachment-{context['attachment_id']}-"))
        source = workdir / f"source.{extension}"
        output = workdir / "mineru"
        try:
            self.store.download(str(context["storage_key"]), source, settings.attachment_parse_max_file_size,
                                bucket=context.get("storage_bucket") or None)
            if extension in SUPPORTED_MINERU:
                markdown, parser = parse_mineru(source, output)
            else:
                markdown, parser = parse_local(source, extension)
            if not markdown.strip():
                raise RuntimeError("document contains no usable text")
            prefix = derived_prefix(context)
            markdown_key, canonical_key = f"{prefix}/document.md", f"{prefix}/canonical.json"
            markdown_path = workdir / "document.md"
            markdown_path.write_text(markdown, encoding="utf-8")
            self.store.upload_file(markdown_key, markdown_path, "text/markdown; charset=utf-8")
            asset_keys: list[str] = []
            image_root = next(iter(sorted(output.rglob("images"))), None) if output.exists() else None
            if image_root and image_root.is_dir():
                for file in image_root.rglob("*"):
                    if file.is_file():
                        key = f"{prefix}/assets/{file.name}"
                        self.store.upload_file(key, file, "application/octet-stream")
                        asset_keys.append(key)
            canonical = {"schema_version": "v1", "attachment_id": context["attachment_id"],
                         "parser": parser, "parser_version": settings.attachment_parser_version,
                         "content": markdown, "headings": _headings(markdown),
                         "derived_markdown_key": markdown_key, "asset_keys": asset_keys}
            self.store.upload_json(canonical_key, canonical)
            return CanonicalDocument(markdown, markdown, canonical["headings"], parser,
                                     settings.attachment_parser_version, markdown_key, canonical_key, asset_keys)
        finally:
            shutil.rmtree(workdir, ignore_errors=True)
