from contextlib import asynccontextmanager
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer
from typing import List, Literal, Optional
import torch
import uvicorn
import os

# to suppress the "forked after parallelism" deadlock warning from HuggingFace tokenizers.
os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")

MODEL_NAME = os.getenv("EMBED_MODEL", "intfloat/multilingual-e5-small")
model: Optional[SentenceTransformer] = None


@asynccontextmanager
async def lifespan(app: FastAPI):
    global model
    print(f"Loading model: {MODEL_NAME}")
    m = SentenceTransformer(MODEL_NAME)
    try:
        m[0].auto_model = torch.compile(m[0].auto_model)
        print("Model compiled with torch.compile.")
    except Exception as e:
        print(f"torch.compile skipped: {e}")
    model = m
    print("Model loaded.")
    yield


app = FastAPI(title="EkşiPulse Embedder", version="1.0.0", lifespan=lifespan)


class EmbedRequest(BaseModel):
    texts: List[str]
    type: Literal["query", "passage"] = "passage"


class EmbedResponse(BaseModel):
    embeddings: List[List[float]]
    dim: int


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest):
    if model is None:
        raise HTTPException(status_code=503, detail="Model not loaded yet")
    if not req.texts:
        return EmbedResponse(embeddings=[], dim=384)

    prefix = "query: " if req.type == "query" else "passage: "
    prefixed = [prefix + t for t in req.texts]

    vecs = model.encode(
        prefixed,
        normalize_embeddings=True,
        batch_size=min(len(prefixed), 32),
        convert_to_tensor=True,
    )
    # Cast back to float32 for serialisation compatibility.
    vecs = vecs.to(torch.float32)
    return EmbedResponse(
        embeddings=vecs.tolist(),
        dim=vecs.shape[1] if vecs.ndim > 1 else 384,
    )


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL_NAME, "ready": model is not None}


if __name__ == "__main__":
    port = int(os.getenv("EMBEDDER_PORT", "8765"))
    uvicorn.run("main:app", host="0.0.0.0", port=port, reload=False, timeout_keep_alive=30)
