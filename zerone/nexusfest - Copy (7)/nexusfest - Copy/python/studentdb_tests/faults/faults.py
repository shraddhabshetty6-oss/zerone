"""PY-08: Fault Injection Engine.

Arms failpoints, times signal delivery, and emulates power-loss using durability.ledger.
"""

from __future__ import annotations
import json
import os
import time
from typing import Any, Dict, List, Optional


class FaultInjectionEngine:
    """Manages failpoint specifications and execution."""

    def __init__(self, data_dir: str) -> None:
        self.data_dir = data_dir

    def build_spec(
        self,
        name: str,
        action: str,
        after_hits: int = 0,
        probability: float = 0.0,
        seed: int = 42,
        once: bool = True,
    ) -> Dict[str, Any]:
        """Build failpoint configuration dictionary."""
        spec: Dict[str, Any] = {
            name: {
                "action": action,
                "after_hits": after_hits,
                "probability": probability,
                "seed": seed,
                "once": once,
            }
        }
        return spec

    def emulate_power_loss(self) -> Optional[int]:
        """Truncate students.wal to the last synced length recorded in durability.ledger."""
        ledger_path = os.path.join(self.data_dir, "durability.ledger")
        wal_path = os.path.join(self.data_dir, "students.wal")

        if not os.path.exists(ledger_path) or not os.path.exists(wal_path):
            return None

        last_wal_synced_len: Optional[int] = None
        with open(ledger_path, "r", encoding="utf-8") as f:
            for line in f:
                parts = line.strip().split()
                if len(parts) == 2 and parts[0] == "students.wal":
                    try:
                        last_wal_synced_len = int(parts[1])
                    except ValueError:
                        pass

        if last_wal_synced_len is not None:
            current_wal_size = os.path.getsize(wal_path)
            if current_wal_size > last_wal_synced_len:
                with open(wal_path, "r+b") as f:
                    f.truncate(last_wal_synced_len)
                return current_wal_size - last_wal_synced_len

        return 0

