"""Document ranks and primary metrics; no service access or gold mutation."""
import json
import math
import sys

from .metrics import summarize

ROUTES = ("bm25", "vector", "psr")
METRICS = ("ndcg@10", "recall@10", "recall@100")


def aggregate_documents(hits, document_map):
    seen_chunks, seen_documents, ranked = set(), set(), []
    for hit in hits:
        chunk, document, score = hit["chunk_id"], hit["document_id"], hit["score"]
        if chunk in seen_chunks:
            raise ValueError("Duplicate chunk across the result window")
        seen_chunks.add(chunk)
        if document not in document_map or not isinstance(score, (int, float)) or not math.isfinite(score):
            raise ValueError("Unknown document or non-finite score")
        original = document_map[document]
        if original in seen_documents:
            continue
        seen_documents.add(original)
        ranked.append({"document_id": original, "rank": len(ranked) + 1,
                       "score": score, "first_chunk_id": chunk, "first_chunk_rank": len(seen_chunks)})
    return ranked[:100]


def score_records(records, qrels):
    from ranx import Qrels, Run, evaluate
    if not qrels or any(not any(value > 0 for value in labels.values()) for labels in qrels.values()):
        raise ValueError("Empty test set or query without positive relevance")
    lookup = {}
    for row in records:
        key = row["route"], row["query_id"]
        if key in lookup:
            raise ValueError("Duplicate route/query result")
        if row["state"] not in ("success", "empty", "failed", "not_attempted"):
            raise ValueError("Invalid terminal state")
        if row["state"] in ("failed", "not_attempted", "empty") and row["documents"]:
            raise ValueError("Failed or empty query cannot retain partial favorable results")
        docs = row["documents"]
        if [d["rank"] for d in docs] != list(range(1, len(docs) + 1)) or len({d["document_id"] for d in docs}) != len(docs) or len(docs) > 100:
            raise ValueError("Document ranking is not a unique top-100 sequence")
        lookup[key] = row
    expected = {(route, qid) for route in ROUTES for qid in qrels}
    if set(lookup) != expected:
        raise ValueError("Full route/query coverage mismatch")
    summary, per_query = {}, []
    for route in ROUTES:
        rows = [lookup[route, qid] for qid in sorted(qrels)]
        nonempty = {row["query_id"]: {d["document_id"]: 101 - d["rank"] for d in row["documents"]} for row in rows if row["documents"]}
        scores = {}
        if nonempty:
            run = Run(nonempty)
            evaluate(Qrels({qid: qrels[qid] for qid in nonempty}), run, list(METRICS), threads=1)
            scores = {name: {str(qid): float(value) for qid, value in run.scores[name].items()} for name in METRICS}
        attempted = [r for r in rows if r["state"] != "not_attempted"]
        timed = [r for r in attempted if r.get("attempt_state") != "unknown"]
        unknown = len(attempted) - len(timed)
        for row in timed:
            if not isinstance(row["elapsed_s"], (int, float)) or not math.isfinite(row["elapsed_s"]) or row["elapsed_s"] < 0:
                raise ValueError("Attempted query lacks valid elapsed time")
        failures = sum(r["state"] == "failed" for r in rows)
        absent = sum(r["state"] == "not_attempted" for r in rows)
        for row in rows:
            values = {name: scores.get(name, {}).get(row["query_id"], 0.0) for name in METRICS}
            per_query.append({"route": route, "query_id": row["query_id"], "state": row["state"],
                              "elapsed_s": row["elapsed_s"], "documents": len(row["documents"]), **values})
        summary[route] = {
            **{name: sum(scores.get(name, {}).get(qid, 0.0) for qid in qrels) / len(qrels) for name in METRICS},
            "expected_queries": len(qrels), "attempted_queries": len(attempted), "failure_count": failures,
            "failure_rate": failures / len(qrels), "not_attempted": absent, "not_attempted_rate": absent / len(qrels),
            "empty_results": sum(r["state"] == "empty" for r in rows),
            "latency_all": summarize([r["elapsed_s"] for r in timed]),
            "latency_success": summarize([r["elapsed_s"] for r in timed if r["state"] in ("success", "empty")]),
            "latency_failed": summarize([r["elapsed_s"] for r in timed if r["state"] == "failed"]),
            "unknown_attempts": unknown, "latency_complete": unknown == 0,
            "full_attempt_coverage": absent == 0 and unknown == 0,
        }
    return {"summary": summary, "per_query": per_query}


if __name__ == "__main__":
    payload = json.load(sys.stdin)
    print(json.dumps(score_records(payload["records"], payload["qrels"]), allow_nan=False))
