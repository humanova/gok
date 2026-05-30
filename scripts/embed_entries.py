#!/usr/bin/env python3
"""
embed_entries.py – Backfill embeddings for eksi entries from the last X days.

Usage:
    python embed_entries.py --days 7
    python embed_entries.py --days 3 --batch-size 64 --embedder http://localhost:8765

Batch size is auto-calculated based on entry count if not specified.
"""

import argparse
import json
import math
import time
from datetime import datetime, timezone, timedelta
from pathlib import Path

import psycopg2
import psycopg2.extras
import requests

CONFIG_PATH = Path(__file__).resolve().parent.parent / "configs" / "config.json"


def load_config():
    with open(CONFIG_PATH) as f:
        return json.load(f)


def get_conn(cfg):
    return psycopg2.connect(
        host=cfg["DbHost"],
        port=cfg["DbPort"],
        dbname=cfg["DbName"],
        user=cfg["DbUser"],
        password=cfg["DbPassword"],
        sslmode=cfg.get("DbSSLMode", "disable"),
    )


def fetch_unembedded_entries(conn, since_ts):
    """Return list of (id, text) for entries after since_ts with no embedding."""
    with conn.cursor(cursor_factory=psycopg2.extras.DictCursor) as cur:
        cur.execute(
            """
            SELECT id, text FROM entries
            WHERE timestamp > %s
              AND embedding IS NULL
              AND deleted_at IS NULL
            ORDER BY id ASC
            """,
            (since_ts,),
        )
        return cur.fetchall()


def embed_texts(embedder_url, texts):
    resp = requests.post(
        f"{embedder_url}/embed",
        json={"texts": texts, "type": "passage"},
        timeout=180,
    )
    resp.raise_for_status()
    return resp.json()["embeddings"]


def update_embeddings(conn, rows):
    """rows: list of (id, embedding_list)"""
    with conn.cursor() as cur:
        psycopg2.extras.execute_batch(
            cur,
            "UPDATE entries SET embedding = %s::vector, embedding_at = NOW() WHERE id = %s",
            [(f"[{','.join(str(v) for v in emb)}]", row_id) for row_id, emb in rows],
        )
    conn.commit()


def calc_batch_size(total, override=None):
    if override:
        return override
    # Scale batch size: small datasets use larger batches, huge ones stay conservative
    if total <= 500:
        return 64
    elif total <= 2000:
        return 48
    elif total <= 10000:
        return 32
    else:
        return 16


def main():
    parser = argparse.ArgumentParser(description="Backfill entry embeddings")
    parser.add_argument("--days", type=float, default=7, help="How many days back to embed (default: 7)")
    parser.add_argument("--batch-size", type=int, default=None, help="Override batch size (auto-calculated if omitted)")
    parser.add_argument("--embedder", type=str, default=None, help="Embedder base URL (overrides config)")
    args = parser.parse_args()

    cfg = load_config()
    embedder_url = args.embedder or cfg.get("EmbedderUrl", "http://localhost:8765")

    since_ts = int((datetime.now(timezone.utc) - timedelta(days=args.days)).timestamp())

    print(f"Connecting to DB {cfg['DbName']} @ {cfg['DbHost']}:{cfg['DbPort']}")
    conn = get_conn(cfg)

    print(f"Fetching unembedded entries since {datetime.fromtimestamp(since_ts, tz=timezone.utc).isoformat()} ({args.days} days)...")
    entries = fetch_unembedded_entries(conn, since_ts)
    total = len(entries)

    if total == 0:
        print("No unembedded entries found.")
        conn.close()
        return

    batch_size = calc_batch_size(total, args.batch_size)
    num_batches = math.ceil(total / batch_size)
    print(f"Found {total} entries → {num_batches} batches of ~{batch_size}")

    done = 0
    for i in range(0, total, batch_size):
        batch = entries[i : i + batch_size]
        ids = [r["id"] for r in batch]
        texts = [r["text"] for r in batch]

        t0 = time.time()
        embeddings = embed_texts(embedder_url, texts)
        elapsed = time.time() - t0

        update_embeddings(conn, list(zip(ids, embeddings)))
        done += len(batch)
        print(f"  [{done}/{total}] batch {i // batch_size + 1}/{num_batches} embedded in {elapsed:.2f}s")

    conn.close()
    print(f"Done. Embedded {done} entries.")


if __name__ == "__main__":
    main()
