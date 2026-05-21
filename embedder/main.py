from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer
from typing import Literal
import uvicorn
import os

app = FastAPI(title="EkşiPulse Embedder", version="1.0.0")

MODEL_NAME = os.getenv("EMBED_MODEL", "intfloat/multilingual-e5-large")
model: SentenceTransformer | None = None


@app.on_event("startup")
def load_model():
    global model
    print(f"Loading model: {MODEL_NAME}")
    model = SentenceTransformer(MODEL_NAME)
    print("Model loaded.")


class EmbedRequest(BaseModel):
    texts: list[str]
    type: Literal["query", "passage"] = "passage"


class EmbedResponse(BaseModel):
    embeddings: list[list[float]]
    dim: int


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest):
    if model is None:
        raise HTTPException(status_code=503, detail="Model not loaded yet")
    if not req.texts:
        return EmbedResponse(embeddings=[], dim=1024)

    # Multilingual-E5-Large requires task prefixes
    prefix = "query: " if req.type == "query" else "passage: "
    prefixed = [prefix + t for t in req.texts]

    vecs = model.encode(prefixed, normalize_embeddings=True, batch_size=32)
    return EmbedResponse(
        embeddings=vecs.tolist(),
        dim=vecs.shape[1] if vecs.ndim > 1 else 1024,
    )


@app.get("/health")
def health():
    return {"status": "ok", "model": MODEL_NAME, "ready": model is not None}


if __name__ == "__main__":
    port = int(os.getenv("EMBEDDER_PORT", "8765"))
    uvicorn.run("main:app", host="0.0.0.0", port=port, reload=False)
