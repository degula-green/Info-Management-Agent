import os


class Settings:
    rag_database_url: str = os.getenv("RAG_DATABASE_URL", "")
    rag_vector_dimension: int = int(os.getenv("RAG_VECTOR_DIMENSION", "1536"))
    processor_version: str = os.getenv("VECTOR_PROCESSOR_VERSION", "v1")
    elasticsearch_url: str = os.getenv("ELASTICSEARCH_URL", "http://127.0.0.1:9200")
    elasticsearch_username: str = os.getenv("ELASTICSEARCH_USERNAME", "")
    elasticsearch_password: str = os.getenv("ELASTICSEARCH_PASSWORD", "")
    elasticsearch_index: str = os.getenv("ELASTICSEARCH_INDEX", "info-agent-documents")
    elasticsearch_verify_certs: bool = os.getenv("ELASTICSEARCH_VERIFY_CERTS", "false").lower() == "true"
    embedding_api_base_url: str = os.getenv("EMBEDDING_API_BASE_URL", "")
    embedding_api_key: str = os.getenv("EMBEDDING_API_KEY", "")
    embedding_model: str = os.getenv("EMBEDDING_MODEL", "text-embedding-v4")
    embedding_dimension: int = int(os.getenv("EMBEDDING_DIMENSION", "1536"))
    max_chunks_per_request: int = int(os.getenv("MAX_CHUNKS_PER_REQUEST", "32"))
    max_chars_per_chunk: int = int(os.getenv("MAX_CHARS_PER_CHUNK", "800"))
    chunk_overlap_chars: int = int(os.getenv("CHUNK_OVERLAP_CHARS", "100"))
    worker_enabled: bool = os.getenv("RAG_WORKER_ENABLED", "true").lower() == "true"
    worker_poll_interval: float = float(os.getenv("RAG_WORKER_POLL_INTERVAL", "5"))
    worker_batch_size: int = int(os.getenv("RAG_WORKER_BATCH_SIZE", "10"))
    message_value_enabled: bool = os.getenv("MESSAGE_VALUE_ENABLED", "true").lower() == "true"
    message_value_api_base_url: str = os.getenv(
        "MESSAGE_VALUE_API_BASE_URL",
        os.getenv("EMBEDDING_API_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
    )
    message_value_api_key: str = os.getenv("MESSAGE_VALUE_API_KEY", os.getenv("EMBEDDING_API_KEY", ""))
    message_value_model: str = os.getenv("MESSAGE_VALUE_MODEL", "qwen-plus")
    message_value_timeout_seconds: float = float(os.getenv("MESSAGE_VALUE_TIMEOUT_SECONDS", "10"))


settings = Settings()
