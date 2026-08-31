from __future__ import annotations

import re
from typing import Any

# WeChat/Feishu commonly serialize reactions as bracketed labels. Keep this
# deliberately narrow: only exact, standalone reaction tokens are rejected.
_REACTION_ONLY = re.compile(
    r"^(?:\[[^\[\]<>\r\n]{1,16}\]|[\U0001F300-\U0001FAFF\u2600-\u27BF])+[\uFE0E\uFE0F\u200D]*$"
)


def is_obvious_noise(message: dict[str, Any]) -> bool:
    raw_type = str(message.get("msg_type") or message.get("message_type") or message.get("type") or "").lower()
    # Only text messages can be safely classified by their displayed content.
    if raw_type and raw_type not in {"text", "1", "txt"}:
        return False
    content = message.get("content")
    if content is None:
        content = message.get("text")
    if not isinstance(content, str):
        return False
    return bool(_REACTION_ONLY.fullmatch(content.strip()))
