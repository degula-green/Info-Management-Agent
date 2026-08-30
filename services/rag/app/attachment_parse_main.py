from __future__ import annotations

import signal
import time

from app.config import settings
from app.services.attachment_worker import AttachmentParseWorker
from app.services.document_parser import AttachmentDocumentParser
from app.services.pgvector_store import PgVectorStore


def main() -> None:
    if not settings.rag_database_url:
        raise RuntimeError("RAG_DATABASE_URL is required")
    workers = [AttachmentParseWorker(PgVectorStore(), AttachmentDocumentParser())
               for _ in range(max(1, settings.attachment_parse_concurrency))]
    for worker in workers:
        worker.start()

    def stop(*_: object) -> None:
        for worker in workers:
            worker.stop()
        raise SystemExit(0)

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)
    while True:
        time.sleep(3600)


if __name__ == "__main__":
    main()
