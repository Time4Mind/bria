from __future__ import annotations

import logging
import os


def configure_logging() -> None:
    level_name = os.getenv("BRIA_LOG_LEVEL", "INFO").upper()
    level = getattr(logging, level_name, None)
    if not isinstance(level, int):
        raise ValueError(f"invalid BRIA_LOG_LEVEL: {level_name}")
    logging.basicConfig(
        level=level,
        format=(
            "%(asctime)s %(levelname)s %(name)s "
            "pid=%(process)d %(message)s"
        ),
    )
