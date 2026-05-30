"""
Topic Pulse — entry velocity heatmap for the last 48h.

Run with:
    ../embedder/.venv/bin/streamlit run topic_pulse.py
"""

import json
import math
from datetime import datetime, timezone, timedelta
from pathlib import Path

import psycopg2
import psycopg2.extras
import pandas as pd
import plotly.graph_objects as go
import streamlit as st

# ── Config ────────────────────────────────────────────────────────────────────
CONFIG_PATH = Path(__file__).parent.parent / "configs" / "config.json"

def load_cfg():
    return json.loads(CONFIG_PATH.read_text())

cfg = load_cfg()

# ── Page config (must be first Streamlit call) ───────────────────────────────
st.set_page_config(page_title="Topic Pulse 🌡️", layout="wide")

# ── DB ────────────────────────────────────────────────────────────────────────
@st.cache_resource
def get_conn():
    return psycopg2.connect(
        host=cfg["DbHost"],
        port=cfg["DbPort"],
        dbname=cfg["DbName"],
        user=cfg["DbUser"],
        password=cfg["DbPassword"],
        sslmode=cfg["DbSSLMode"],
    )

def query(sql, params=None):
    conn = get_conn()
    with conn.cursor(cursor_factory=psycopg2.extras.RealDictCursor) as cur:
        cur.execute(sql, params)
        return cur.fetchall()

# ── Data loading ──────────────────────────────────────────────────────────────
@st.cache_data(ttl=120)
def load_velocity(hours_back: int, bucket_minutes: int, min_entries: int):
    """
    Returns a wide DataFrame: rows = topics, columns = time-bucket labels,
    values = entry count per bucket.
    Also returns a topic-name map {topic_id: title}.
    """
    since_ts = int((datetime.now(timezone.utc) - timedelta(hours=hours_back)).timestamp())

    # Entry counts per topic per time bucket
    rows = query(
        """
        SELECT
            e.topic_id,
            (e.timestamp / (%s * 60)) * (%s * 60)  AS bucket_ts,
            COUNT(*)                                 AS cnt
        FROM entries e
        WHERE e.timestamp >= %s
          AND e.deleted_at IS NULL
        GROUP BY e.topic_id, bucket_ts
        ORDER BY e.topic_id, bucket_ts
        """,
        (bucket_minutes, bucket_minutes, since_ts),
    )

    if not rows:
        return pd.DataFrame(), {}

    df = pd.DataFrame(rows, columns=["topic_id", "bucket_ts", "cnt"])
    df["bucket_ts"] = df["bucket_ts"].astype(int)
    df["cnt"] = df["cnt"].astype(int)

    # Filter topics with enough total entries
    totals = df.groupby("topic_id")["cnt"].sum()
    active = totals[totals >= min_entries].index
    df = df[df["topic_id"].isin(active)]

    if df.empty:
        return pd.DataFrame(), {}

    # Build complete time-bucket index
    now_ts = int(datetime.now(timezone.utc).timestamp())
    step = bucket_minutes * 60
    all_buckets = list(range(
        (since_ts // step) * step,
        (now_ts  // step) * step + step,
        step,
    ))

    # Pivot: rows = topic_id, columns = bucket_ts
    pivot = df.pivot(index="topic_id", columns="bucket_ts", values="cnt").reindex(
        columns=all_buckets, fill_value=0
    ).fillna(0).astype(int)

    # Fetch topic titles
    topic_ids = list(pivot.index)
    placeholders = ",".join(["%s"] * len(topic_ids))
    title_rows = query(
        f"SELECT topic_id, text FROM topics WHERE topic_id IN ({placeholders}) AND deleted_at IS NULL",
        topic_ids,
    )
    title_map = {r["topic_id"]: r["text"] for r in title_rows}

    # Sort by total desc
    pivot["_total"] = pivot.sum(axis=1)
    pivot = pivot.sort_values("_total", ascending=False).drop(columns="_total")

    # Format column labels as HH:MM
    bucket_labels = [
        datetime.fromtimestamp(b, tz=timezone.utc).strftime("%d/%m %H:%M")
        for b in pivot.columns
    ]
    pivot.columns = bucket_labels

    # Map index to titles (fallback to id)
    pivot.index = [title_map.get(tid, f"#{tid}") for tid in pivot.index]

    return pivot, title_map

@st.cache_data(ttl=120)
def load_sparklines(hours_back: int, bucket_minutes: int, top_n: int):
    """Returns a dict of {title: Series(bucket_label -> count)} for top_n topics."""
    pivot, _ = load_velocity(hours_back, bucket_minutes, min_entries=1)
    if pivot.empty:
        return {}
    totals = pivot.sum(axis=1).sort_values(ascending=False)
    top = totals.head(top_n).index
    return {t: pivot.loc[t] for t in top}

@st.cache_data(ttl=120)
def load_topic_list():
    rows = query(
        """
        SELECT t.topic_id, t.text AS title, t.url,
               COUNT(e.id) AS total_entries
        FROM topics t
        JOIN entries e ON e.topic_id = t.topic_id
        WHERE t.deleted_at IS NULL AND e.deleted_at IS NULL
          AND e.timestamp >= EXTRACT(EPOCH FROM NOW() - INTERVAL '48 hours')
        GROUP BY t.topic_id, t.text, t.url
        ORDER BY total_entries DESC
        LIMIT 200
        """
    )
    return rows

# ── UI ────────────────────────────────────────────────────────────────────────
st.title("🌡️ Topic Pulse")
st.caption("Entry velocity per topic — last 48 h · auto-refreshes every 2 min")

# Sidebar controls
with st.sidebar:
    st.header("Controls")
    hours_back    = st.slider("Window (hours)", 6, 48, 48, step=6)
    bucket_min    = st.selectbox("Bucket size", [15, 30, 60, 120], index=1, format_func=lambda x: f"{x} min")
    top_n         = st.slider("Max topics", 10, 60, 30)
    min_entries   = st.number_input("Min entries (filter)", min_value=1, value=5, step=1)
    show_sparklines = st.checkbox("Show sparklines", value=True)
    st.divider()
    api_port      = cfg.get("ApiPort", 8181)
    rag_base      = st.text_input("RAG base URL", value=f"http://localhost:{api_port}")
    st.caption("Topic links open the RAG /chat view")

pivot, title_map = load_velocity(hours_back, bucket_min, min_entries)

if pivot.empty:
    st.warning("No entries found for the selected window. Is the scraper running?")
    st.stop()

# Trim to top_n rows
pivot = pivot.head(top_n)

# ── Heatmap ───────────────────────────────────────────────────────────────────
st.subheader("Entry Velocity Heatmap")

# Log-scale values for colour so spikes don't wash out low-activity cells
import numpy as np
z_raw  = pivot.values.astype(float)
z_log  = np.log1p(z_raw)

# Custom hover text: show actual count
hover = [[f"{int(z_raw[r,c])} entries" for c in range(z_raw.shape[1])]
         for r in range(z_raw.shape[0])]

fig_heat = go.Figure(go.Heatmap(
    z=z_log,
    x=list(pivot.columns),
    y=list(pivot.index),
    customdata=z_raw,
    hovertemplate="%{y}<br>%{x}<br><b>%{customdata:.0f} entries</b><extra></extra>",
    colorscale="YlOrRd",
    showscale=True,
    colorbar=dict(title="log(entries+1)"),
))

row_h = max(18, min(28, 600 // len(pivot)))
fig_heat.update_layout(
    height=max(350, row_h * len(pivot) + 80),
    margin=dict(l=10, r=10, t=30, b=60),
    xaxis=dict(tickangle=-45, tickfont=dict(size=10)),
    yaxis=dict(tickfont=dict(size=11), autorange="reversed"),
    plot_bgcolor="#0e1117",
    paper_bgcolor="#0e1117",
    font_color="white",
)
st.plotly_chart(fig_heat, use_container_width=True)

# ── Sparklines ────────────────────────────────────────────────────────────────
if show_sparklines:
    st.subheader("Top Topic Sparklines")
    sparklines = {t: pivot.loc[t] for t in pivot.index[:min(top_n, len(pivot))]}

    cols_per_row = 3
    keys = list(sparklines.keys())
    for row_start in range(0, len(keys), cols_per_row):
        cols = st.columns(cols_per_row)
        for col, topic_title in zip(cols, keys[row_start:row_start + cols_per_row]):
            series = sparklines[topic_title]
            with col:
                fig_spark = go.Figure(go.Scatter(
                    x=list(series.index),
                    y=list(series.values),
                    mode="lines",
                    fill="tozeroy",
                    line=dict(color="#ff6b35", width=1.5),
                    fillcolor="rgba(255,107,53,0.15)",
                    hovertemplate="%{x}<br><b>%{y} entries</b><extra></extra>",
                ))
                peak = int(series.max())
                fig_spark.update_layout(
                    title=dict(text=f"<b>{topic_title[:40]}</b>", font=dict(size=11)),
                    height=140,
                    margin=dict(l=0, r=0, t=30, b=20),
                    xaxis=dict(showticklabels=False, showgrid=False),
                    yaxis=dict(showticklabels=True, showgrid=False, tickfont=dict(size=9)),
                    plot_bgcolor="#0e1117",
                    paper_bgcolor="#161b22",
                    font_color="white",
                    showlegend=False,
                )
                st.plotly_chart(fig_spark, use_container_width=True)
                st.caption(f"peak {peak} entries/bucket")

# ── Topic table with RAG links ─────────────────────────────────────────────────
st.subheader("Topics (click to open RAG chat)")

topic_rows = load_topic_list()
if topic_rows:
    table_data = []
    for r in topic_rows:
        title = r["title"]
        topic_id = r["topic_id"]
        total = r["total_entries"]
        rag_url = f"{rag_base.rstrip('/')}/topics/{topic_id}/chat"
        table_data.append({
            "Topic": f'<a href="{rag_url}" target="_blank">{title}</a>',
            "Entries (48h)": total,
        })

    df_table = pd.DataFrame(table_data)
    st.write(
        df_table.to_html(escape=False, index=False),
        unsafe_allow_html=True,
    )
else:
    st.info("No topics found.")

# ── Footer ─────────────────────────────────────────────────────────────────────
st.divider()
st.caption(f"Data window: last {hours_back}h · bucket: {bucket_min} min · "
           f"updated: {datetime.now(timezone.utc).strftime('%Y-%m-%d %H:%M UTC')}")
