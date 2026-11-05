# Smoke summary — 2026-11-05 Asia/Singapore

- Dataset: `bre200-es-smoke` id `deb76214d90311f1aaf1f53e5c9c2fb5`
- Docs: sample.md + sample.pdf → parse **DONE** (1 chunk each)
- Retrieve q=`What RAGFlow pin version does PSR use on Mac Studio?` → **code 0**, hit sample.md (sim≈0.37, vector≈0.93)
- Negative retrieve high threshold → **total 0**
- Negative auth → **401**
- Restart recovery → login + list datasets OK; G0 health OK; Plumb PID 92856
- Published ports: 16380,19200,19680-19684,23306,26379,29000-29001 — **no 5432/8787**
