#!/usr/bin/env python3
"""Soulacy's local voice HTTP adapter.

The adapter intentionally keeps speech dependencies outside the Go gateway.
It supports MLX Whisper on Apple Silicon and whisper.cpp's whisper-cli on
portable hosts, while Kokoro supplies local speech synthesis.
"""

from __future__ import annotations

import argparse
import importlib.util
import io
import json
import os
import shutil
import subprocess
import tempfile
import wave
from pathlib import Path

import numpy as np
from fastapi import FastAPI, File, Form, HTTPException, UploadFile
from fastapi.responses import Response
from pydantic import BaseModel
import uvicorn

APP = FastAPI(title="Soulacy local voice sidecar", docs_url=None, redoc_url=None)
STT_BACKEND = os.getenv("SOULACY_VOICE_STT", "mlx")
DEFAULT_VOICE = os.getenv("SOULACY_VOICE_VOICE", "af_heart")
MLX_MODEL = os.getenv("SOULACY_VOICE_MLX_MODEL", "mlx-community/whisper-small-mlx")
WHISPER_BIN = os.getenv("SOULACY_VOICE_WHISPER_BIN", "whisper-cli")
WHISPER_MODEL = os.getenv("SOULACY_VOICE_WHISPER_MODEL", "")
ACCELERATOR = os.getenv("SOULACY_VOICE_ACCELERATOR", "auto").strip().lower()
_KOKORO = None
_TTS_DEVICE = None
MAX_AUDIO_BYTES = 16 << 20
MAX_TEXT_CHARS = 32 << 10


class SynthesisRequest(BaseModel):
    text: str
    voice: str = ""


def _command(name: str) -> str:
    found = shutil.which(name)
    if not found:
        raise RuntimeError(f"required command not found: {name}")
    return found


def _to_wav(data: bytes, suffix: str) -> Path:
    ffmpeg = _command("ffmpeg")
    source = tempfile.NamedTemporaryFile(suffix=suffix, delete=False)
    target = Path(source.name).with_suffix(".wav")
    try:
        source.write(data)
        source.close()
        run = subprocess.run(
            [ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", source.name,
             "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", str(target)],
            capture_output=True, text=True, timeout=60,
        )
        if run.returncode:
            raise RuntimeError(run.stderr.strip() or "ffmpeg could not decode the recording")
        return target
    finally:
        Path(source.name).unlink(missing_ok=True)


def _transcribe_mlx(wav_path: Path) -> str:
    import mlx_whisper

    result = mlx_whisper.transcribe(str(wav_path), path_or_hf_repo=MLX_MODEL)
    return str(result.get("text", "")).strip()


def _transcribe_whisper_cpp(wav_path: Path) -> str:
    if not WHISPER_MODEL:
        raise RuntimeError("SOULACY_VOICE_WHISPER_MODEL must point to a ggml model")
    command = [_command(WHISPER_BIN), "-m", WHISPER_MODEL, "-f", str(wav_path), "-nt"]
    # GPU is whisper.cpp's default when its binary was compiled with Metal,
    # CUDA, or Vulkan. Explicit CPU mode remains useful for troubleshooting.
    if ACCELERATOR == "cpu":
        command.append("-ng")
    run = subprocess.run(
        command,
        capture_output=True, text=True, timeout=180,
    )
    if run.returncode:
        raise RuntimeError(run.stderr.strip() or "whisper.cpp transcription failed")
    return run.stdout.strip()


def _tts_device() -> str:
    global _TTS_DEVICE
    if _TTS_DEVICE is not None:
        return _TTS_DEVICE
    import torch

    if ACCELERATOR not in {"auto", "cpu", "mps", "cuda"}:
        raise RuntimeError("SOULACY_VOICE_ACCELERATOR must be auto, cpu, mps, or cuda")
    if ACCELERATOR == "cuda":
        if not torch.cuda.is_available():
            raise RuntimeError("CUDA was requested but PyTorch cannot access a CUDA GPU")
        _TTS_DEVICE = "cuda"
    elif ACCELERATOR == "mps":
        if not hasattr(torch.backends, "mps") or not torch.backends.mps.is_available():
            raise RuntimeError("MPS was requested but PyTorch cannot access Apple Metal")
        _TTS_DEVICE = "mps"
    elif ACCELERATOR == "cpu":
        _TTS_DEVICE = "cpu"
    elif torch.cuda.is_available():
        _TTS_DEVICE = "cuda"
    elif hasattr(torch.backends, "mps") and torch.backends.mps.is_available():
        _TTS_DEVICE = "mps"
    else:
        _TTS_DEVICE = "cpu"
    return _TTS_DEVICE


def _stt_accelerator() -> str:
    if STT_BACKEND == "mlx":
        return "metal"
    if ACCELERATOR == "cpu":
        return "cpu"
    return "native-auto"


def _kokoro_pipeline():
    global _KOKORO
    if _KOKORO is None:
        from kokoro import KPipeline
        _KOKORO = KPipeline(lang_code="a", device=_tts_device())
    return _KOKORO


def _readiness_error() -> str:
    if not shutil.which("ffmpeg"):
        return "ffmpeg is not installed"
    if importlib.util.find_spec("kokoro") is None:
        return "Kokoro is not installed"
    if STT_BACKEND == "mlx":
        if importlib.util.find_spec("mlx_whisper") is None:
            return "MLX Whisper is not installed"
    else:
        if not shutil.which(WHISPER_BIN):
            return f"whisper.cpp command not found: {WHISPER_BIN}"
        if not WHISPER_MODEL or not Path(WHISPER_MODEL).is_file():
            return "SOULACY_VOICE_WHISPER_MODEL must name an existing ggml model"
    return ""


def _wav_bytes(text: str, voice: str) -> bytes:
    chunks = []
    for _, _, audio in _kokoro_pipeline()(text, voice=voice or DEFAULT_VOICE):
        chunks.append(np.asarray(audio, dtype=np.float32))
    if not chunks:
        raise RuntimeError("Kokoro returned no audio")
    samples = np.clip(np.concatenate(chunks), -1.0, 1.0)
    pcm = (samples * 32767.0).astype("<i2").tobytes()
    out = io.BytesIO()
    with wave.open(out, "wb") as wav:
        wav.setnchannels(1)
        wav.setsampwidth(2)
        wav.setframerate(24000)
        wav.writeframes(pcm)
    return out.getvalue()


def _warm_models() -> None:
    """Pay model download/load cost before the sidecar reports ready."""
    print(f"Warming {STT_BACKEND} speech recognition…", flush=True)
    if STT_BACKEND == "mlx":
        silent = tempfile.NamedTemporaryFile(suffix=".wav", delete=False)
        silent.close()
        silent_path = Path(silent.name)
        try:
            with wave.open(str(silent_path), "wb") as wav:
                wav.setnchannels(1)
                wav.setsampwidth(2)
                wav.setframerate(16000)
                wav.writeframes(b"\x00\x00" * 8000)
            _transcribe_mlx(silent_path)
        finally:
            silent_path.unlink(missing_ok=True)
    print("Warming Kokoro speech synthesis…", flush=True)
    _wav_bytes("Voice is ready.", DEFAULT_VOICE)
    print(
        f"Voice models ready (STT: {_stt_accelerator()}, TTS: {_tts_device()}).",
        flush=True,
    )


@APP.get("/health")
def health():
    if problem := _readiness_error():
        raise HTTPException(503, problem)
    return {
        "status": "ok", "stt_backend": STT_BACKEND,
        "stt_accelerator": _stt_accelerator(), "tts_device": _tts_device(),
    }


@APP.get("/capabilities")
def capabilities():
    return {
        "stt": True, "tts": True, "streaming": False,
        "languages": ["en"], "voices": [DEFAULT_VOICE],
        "stt_backend": STT_BACKEND, "stt_accelerator": _stt_accelerator(),
        "tts_backend": "kokoro", "tts_device": _tts_device(),
    }


@APP.post("/transcribe")
async def transcribe(audio: UploadFile = File(...), content_type: str = Form("")):
    data = await audio.read()
    if not data:
        raise HTTPException(400, "audio is empty")
    if len(data) > MAX_AUDIO_BYTES:
        raise HTTPException(413, "audio exceeds 16 MiB limit")
    suffix = ".webm"
    kind = content_type or audio.content_type or ""
    if "ogg" in kind:
        suffix = ".ogg"
    elif "mp4" in kind:
        suffix = ".m4a"
    wav_path = _to_wav(data, suffix)
    try:
        text = _transcribe_mlx(wav_path) if STT_BACKEND == "mlx" else _transcribe_whisper_cpp(wav_path)
        if not text:
            raise RuntimeError("speech recognizer returned no transcript")
        return {"text": text}
    except Exception as exc:
        raise HTTPException(502, str(exc)) from exc
    finally:
        wav_path.unlink(missing_ok=True)


@APP.post("/synthesize")
def synthesize(request: SynthesisRequest):
    if not request.text.strip():
        raise HTTPException(400, "text is required")
    if len(request.text.encode("utf-8")) > MAX_TEXT_CHARS:
        raise HTTPException(413, "text exceeds 32 KiB limit")
    try:
        return Response(_wav_bytes(request.text, request.voice), media_type="audio/wav")
    except Exception as exc:
        raise HTTPException(502, str(exc)) from exc


def main() -> None:
    parser = argparse.ArgumentParser(description="Soulacy local voice sidecar")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8081)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    if args.check:
        if problem := _readiness_error():
            raise SystemExit(problem)
        print(json.dumps({
            "ready": True, "stt": STT_BACKEND, "tts": "kokoro",
            "stt_accelerator": _stt_accelerator(), "tts_device": _tts_device(),
        }))
        return
    if os.getenv("SOULACY_VOICE_WARMUP", "1").lower() not in {"0", "false", "no"}:
        _warm_models()
    uvicorn.run(APP, host=args.host, port=args.port, log_level="info")


if __name__ == "__main__":
    main()
