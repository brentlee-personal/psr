"""Controlled v2 verifier, independently derived from the frozen v1 verifier."""

import argparse
import ast
import collections
import csv
import datetime
import hashlib
import itertools
import json
import math
import subprocess
import sys
import zipfile
from pathlib import Path

IDENTITY = "/root/independent_eval"
ROUTES = ("bm25", "vector", "psr")
METRICS = ("ndcg@10", "recall@10", "recall@100")
TOLERANCE = 1e-6
ES_ADAPTER = "common/doc_store/es_conn_base.py"
REQUIRED_RUNTIME_PATHS = {
    ES_ADAPTER, "rag/nlp/query.py", "rag/nlp/synonym.py", "rag/nlp/term_weight.py",
    "rag/nlp/search.py", "rag/nlp/rag_tokenizer.py", "rag/utils/es_conn.py", "rag/llm/embedding_model.py",
    "api/db/services/llm_service.py", "api/apps/restful_apis/chunk_api.py",
}
NLP_FIELDS = ("python_hash_seed", "effective_synonyms_sha256", "redis_synonyms_sha256", "redis_synonyms_state", "wordnet_version",
              "wordnet_assets", "lexical_assets", "lexical_asset_presence", "infinity_tokenizer_source_sha256", "infinity_sdk_version")


def validate_nlp_states(nlp):
    absent = hashlib.sha256(b"ABSENT:kevin_synonyms").hexdigest()
    empty = hashlib.sha256(b"").hexdigest()
    state = nlp["redis_synonyms_state"]
    digest = nlp["redis_synonyms_sha256"]
    if state not in {"absent", "empty", "present"}:
        raise ValueError("unknown Redis synonym state; read failures cannot be frozen as absence")
    if (state == "absent" and digest != absent) or (state == "empty" and digest != empty):
        raise ValueError("Redis absent/empty state does not match its canonical digest")
    if state == "present" and digest in {absent, empty}:
        raise ValueError("present Redis synonyms cannot use an absent/empty digest")
    assets = {Path(path).name: digest for path, digest in nlp["lexical_assets"].items()}
    presence = {Path(path).name: exists for path, exists in nlp["lexical_asset_presence"].items()}
    if len(assets) != len(nlp["lexical_assets"]) or len(presence) != len(nlp["lexical_asset_presence"]):
        raise ValueError("ambiguous lexical asset basenames")
    for path in ("synonym.json", "term.freq", "ner.json"):
        if path not in presence or not isinstance(presence[path], bool):
            raise ValueError(f"missing explicit lexical asset presence: {path}")
        if presence[path] != (path in assets):
            raise ValueError(f"lexical asset presence and digest disagree: {path}")


def runtime_origins(run, protocol, run_name):
    control = protocol.get("controlled_v2", {})
    if control.get("schema") != "psr-beir-controlled-v2":
        raise ValueError("explicit controlled_v2 schema is required")
    if run["code_sha"] != control.get("controller_code_sha"):
        raise ValueError("controller code SHA differs from frozen controlled_v2 SHA")
    declarations = run.get("runtime_sources", {})
    observed = run["runtime"]["source_hashes"]
    if set(declarations) != set(observed):
        raise ValueError("runtime_sources must cover exactly the observed runtime source set")
    if not REQUIRED_RUNTIME_PATHS <= observed.keys():
        raise ValueError("required runtime source hashes are missing")
    overrides = control.get("variants", {}).get(run_name, {}).get("runtime_overrides", {})
    if (run_name == "baseline" and set(overrides) != {ES_ADAPTER}) or (run_name == "candidate" and overrides):
        raise ValueError("controlled variants require exactly baseline ES override and no candidate override")
    if set(overrides) - {ES_ADAPTER}:
        raise ValueError("only the ES adapter may be an intentional runtime override")
    origins = {}
    for path, declaration in declarations.items():
        if declaration.get("sha256") != observed[path]:
            raise ValueError(f"declared and observed hash differ: {path}")
        if path in overrides:
            expected = overrides[path]
            if declaration.get("role") != "intentional_override" or not declaration.get("reason"):
                raise ValueError(f"runtime override needs explicit role and reason: {path}")
            if any(declaration.get(key) != expected.get(key) for key in ("source_commit", "sha256", "reason")):
                raise ValueError(f"runtime override differs from frozen protocol: {path}")
        elif declaration.get("role") != "controller" or declaration.get("source_commit") != run["code_sha"]:
            raise ValueError(f"undeclared runtime source override: {path}")
        commit = declaration.get("source_commit", "")
        if len(commit) != 40 or any(c not in "0123456789abcdef" for c in commit):
            raise ValueError(f"runtime source requires full commit SHA: {path}")
        origins[path] = commit
    return origins


def timing_source_check(source_bytes):
    tree = ast.parse(source_bytes)
    matches = []
    for node in ast.walk(tree):
        if not isinstance(node, ast.For):
            continue
        direct = [child for statement in node.body for child in ast.walk(statement)]
        attempts = [child.lineno for child in direct if isinstance(child, ast.Call)
                    and isinstance(child.func, ast.Name) and child.func.id == "write_json"
                    and child.args and ".attempt.json" in ast.unparse(child.args[0])]
        starts = [child.lineno for child in direct if isinstance(child, ast.Assign)
                  and any(isinstance(t, ast.Name) and t.id == "started" for t in child.targets)
                  and isinstance(child.value, ast.Call) and ast.unparse(child.value.func) == "time.perf_counter"]
        dispatches = [child.lineno for child in direct if isinstance(child, ast.Call)
                      and isinstance(child.func, ast.Attribute) and child.func.attr == "call"
                      and child.args and isinstance(child.args[0], ast.Constant) and child.args[0].value == "query"]
        if len(attempts) == len(starts) == len(dispatches) == 1:
            matches.append({"attempt_write_line": attempts[0], "timer_start_line": starts[0],
                            "query_dispatch_line": dispatches[0], "ordered": attempts[0] < starts[0] < dispatches[0]})
    if not matches or not all(item["ordered"] for item in matches):
        raise ValueError("source does not establish attempt persistence before query timer before dispatch")
    return min(matches, key=lambda item: item["query_dispatch_line"] - item["attempt_write_line"])


def preflight(evidence, output, manifest_path=None):
    errors, observations, source_checks = [], {}, []
    protocol = load(evidence / "protocol.json")
    control = protocol.get("controlled_v2", {})
    repository = evidence.parents[2]
    manifest = load(manifest_path or evidence / "preflight.json")
    if manifest.get("schema") != "psr-beir-preflight-v2":
        errors.append({"reason": "preflight manifest schema is not psr-beir-preflight-v2"})
    if manifest.get("code_sha") != control.get("controller_code_sha") or manifest.get("protocol_sha256") != sha(evidence / "protocol.json"):
        errors.append({"reason": "preflight manifest controller or protocol identity differs"})
    variants = manifest.get("variants", {})
    if set(variants) != {"baseline", "candidate"}:
        errors.append({"reason": "preflight requires exactly baseline and candidate variants"})
    timing = None
    for name in ("baseline", "candidate"):
        try:
            observation = variants[name]
            run = {"code_sha": manifest["code_sha"], "runtime": observation["runtime"],
                   "runtime_sources": observation["runtime_sources"]}
            origins = runtime_origins(run, protocol, name)
            for relative, commit in origins.items():
                content = subprocess.check_output(["git", "-C", str(repository), "show", commit + ":" + relative])
                actual = hashlib.sha256(content).hexdigest()
                if actual != run["runtime"]["source_hashes"][relative]:
                    raise ValueError(f"git source hash mismatch: {relative}")
                source_checks.append({"run": name, "path": relative, "source_commit": commit, "sha256": actual})
            controller = subprocess.check_output(["git", "-C", str(repository), "show", run["code_sha"] + ":test/benchmark/beir.py"])
            timing = timing_source_check(controller)
            container = observation["container"]
            nlp = observation["runtime"]["nlp"]
            if any(field not in nlp for field in NLP_FIELDS):
                raise ValueError("required NLP observation is missing")
            validate_nlp_states(nlp)
            if str(container["python_hash_seed"]) != "0" or str(nlp["python_hash_seed"]) != "0":
                raise ValueError("PYTHONHASHSEED must be observed as 0")
            if not container["id"] or not container["name"] or container["published_ports"]:
                raise ValueError("missing container identity or unexpected published ports")
            if nlp != protocol.get("nlp"):
                raise ValueError("observed NLP fingerprint differs from frozen protocol")
            if observation["vector_values_sha256"] != protocol.get("index_vector_sha256"):
                raise ValueError("observed vector values differ from frozen protocol")
            if observation["mapping_sha256"] != sha(evidence / "chunk-map.json"):
                raise ValueError("observed mapping differs from saved full chunk map")
            if observation["model_sentinels"] != protocol.get("model_sentinels"):
                raise ValueError("model sentinel observations differ from frozen protocol")
            if set(observation["model_sentinels"]) != set(protocol["execution"]["warmup"]):
                raise ValueError("sentinel inputs must be exactly the non-test warmup texts")
            digests = [observation["vector_values_sha256"], observation["mapping_sha256"],
                       nlp["effective_synonyms_sha256"], nlp["redis_synonyms_sha256"], nlp["infinity_tokenizer_source_sha256"],
                       *nlp["wordnet_assets"].values(), *nlp["lexical_assets"].values(), *observation["model_sentinels"].values()]
            if not nlp["wordnet_assets"]:
                raise ValueError("empty WordNet asset manifest")
            if any(not isinstance(d, str) or len(d) != 64 or any(c not in "0123456789abcdef" for c in d) for d in digests):
                raise ValueError("invalid resource/vector/sentinel SHA256")
            observations[name] = observation
        except (OSError, KeyError, ValueError, subprocess.CalledProcessError) as exc:
            errors.append({"run": name, "reason": str(exc)})
    if set(observations) == {"baseline", "candidate"}:
        for field in ("index_configuration", "vector_values_sha256", "mapping_sha256", "model_sentinels"):
            if observations["baseline"][field] != observations["candidate"][field]:
                errors.append({"field": field, "reason": "controlled observations differ"})
        for field in ("image_id", "platform", "memory_bytes", "nano_cpus", "python_hash_seed", "published_ports"):
            if observations["baseline"]["container"][field] != observations["candidate"]["container"][field]:
                errors.append({"field": "container." + field, "reason": "container controls differ"})
        if observations["baseline"]["container"]["id"] == observations["candidate"]["container"]["id"]:
            errors.append({"field": "container_id", "reason": "controlled variants must be separate containers"})
    if any((evidence / name / "raw").exists() and any((evidence / name / "raw").rglob("*.json")) for name in ("baseline", "candidate")):
        errors.append({"reason": "test attempts already exist; this cannot grant a pre-run gate retroactively"})
    receipt = {"schema": "psr-beir-independent-preflight-v2", "identity": IDENTITY,
        "evaluator_id": IDENTITY, "verdict": "FAIL" if errors else "PARTIAL", "failures": errors,
        "tool_sha256": sha(__file__), "generated_at": datetime.datetime.now(datetime.UTC).isoformat(),
        "protocol_sha256": sha(evidence / "protocol.json"), "controller_code_sha": control.get("controller_code_sha"),
        "input_manifest_sha256": sha(manifest_path or evidence / "preflight.json"),
        "automatic_checks": "PASS" if not errors else "FAIL", "approval": "PENDING_INDEPENDENT_REVIEW",
        "errors": errors, "source_checks": source_checks, "timing_source": timing, "observations": observations,
        "scope": "Structural checks alone do not approve unknown resource manifests or service sentinel meaning."}
    output.mkdir(parents=True, exist_ok=True)
    (output / "preflight-verdict.json").write_text(json.dumps(receipt, indent=2, ensure_ascii=False) + "\n")
    print(json.dumps({key: receipt[key] for key in ("automatic_checks", "approval", "errors")}, indent=2))
    return receipt


def term_change_summary(changes):
    return {"term_changed_chunks": len(changes),
            "term_changed_query_count": len({row["query_id"] for row in changes}),
            "term_changed_query_ids": sorted({row["query_id"] for row in changes}),
            "term_max_absolute_change": max((abs(row["difference"]) for row in changes), default=0.0),
            "term_max_change_example": max(changes, key=lambda row: abs(row["difference"]), default=None)}


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key {key}")
        result[key] = value
    return result


def load(path):
    return json.loads(Path(path).read_text(), object_pairs_hook=unique_object)


def percentile(values, fraction):
    return sorted(values)[math.ceil(len(values) * fraction) - 1] if values else None


def score(ranking, labels):
    relevant = {doc: gain for doc, gain in labels.items() if gain > 0}
    ideal = sum(gain / math.log2(rank + 2) for rank, gain in
                enumerate(sorted(relevant.values(), reverse=True)[:10]))
    actual = sum(relevant.get(doc, 0) / math.log2(rank + 2)
                 for rank, doc in enumerate(ranking[:10]))
    return {"ndcg@10": actual / ideal if ideal else 0.0,
            "recall@10": len(set(ranking[:10]) & relevant.keys()) / len(relevant) if relevant else 0.0,
            "recall@100": len(set(ranking[:100]) & relevant.keys()) / len(relevant) if relevant else 0.0}


def reconstruct(record, route, doc_map, chunk_map):
    if record is None:
        return [], "unattempted", []
    if record.get("state") == "not_attempted":
        return [], "unattempted", []
    if record.get("state") not in ("success", "empty"):
        return [], "failed", []
    try:
        if record.get("error"):
            raise ValueError("terminal record carries an error")
        raw = record["raw"]
        if raw.get("error") or raw.get("timed_out") or raw.get("_shards", {}).get("failed", 0):
            raise ValueError("raw response contains a partial error")
        if route == "psr":
            pages = raw["pages"]
            if not isinstance(pages, list) or not pages:
                raise ValueError("missing PSR HTTP pages")
            hits = []
            for page in pages:
                response = page["response"]
                if page.get("http_status", 200) != 200 or response.get("code") != 0 or response.get("error"):
                    raise ValueError("unsuccessful HTTP/business response")
                chunks = response["data"]["chunks"]
                if not isinstance(chunks, list):
                    raise TypeError("PSR chunks are not a list")
                hits.extend({"chunk_id": c["id"], "document_id": c["document_id"],
                             "score": c["similarity"]} for c in chunks)
        else:
            hits = raw["hits"]
            if not isinstance(hits, list):
                raise ValueError("native hits are not a list")
        ranking, seen, seen_chunks = [], set(), set()
        for hit in hits:
            chunk, system_doc = str(hit["chunk_id"]), str(hit["document_id"])
            if chunk in seen_chunks:
                raise ValueError(f"duplicate returned chunk {chunk}")
            seen_chunks.add(chunk)
            if not math.isfinite(float(hit["score"])):
                raise ValueError("nonfinite retrieval score")
            if chunk_map.get(chunk) != system_doc:
                raise ValueError(f"chunk mapping mismatch {chunk}")
            doc = doc_map[system_doc]
            if doc not in seen:
                seen.add(doc)
                if len(ranking) < 100:
                    ranking.append(doc)
        if route == "psr" and raw.get("hits") != hits:
            raise ValueError("PSR native HTTP chunks and stored extracted hits disagree")
        if raw.get("returned_chunks") != len(hits):
            raise ValueError("returned chunk count mismatch")
        return ranking, "success" if ranking else "empty", []
    except (KeyError, TypeError, ValueError) as error:
        return [], "failed", [str(error)]


def metric_differences(expected, actual):
    differences = []
    maximum = 0.0
    for key in expected.keys() | actual.keys():
        if key not in expected or key not in actual:
            differences.append({"key": key, "reason": "missing pair"})
            continue
        for metric in METRICS:
            delta = abs(expected[key][metric] - actual[key][metric])
            maximum = max(maximum, delta)
            if not math.isfinite(delta) or delta > TOLERANCE:
                differences.append({"key": key, "metric": metric, "absolute_error": delta})
    return maximum, differences


def self_test():
    labels = {"same": 3, "b": 1}
    result = score(["x", "same", "b"], labels)
    assert abs(result["ndcg@10"] - (3 / math.log2(3) + 0.5) / (3 + 1 / math.log2(3))) < 1e-12
    assert result["recall@10"] == 1
    docs = {str(i): str(i) for i in range(110)}
    chunks = {f"c{i}": str(i) for i in range(110)}
    hits = [{"chunk_id": f"c{i}", "document_id": str(i), "score": 1} for i in range(110)]
    chunks["second_chunk_of_doc0"] = "0"
    hits.insert(1, {"chunk_id": "second_chunk_of_doc0", "document_id": "0", "score": 1})
    record = {"state": "success", "raw": {"hits": hits, "returned_chunks": len(hits)}}
    ranking, state, errors = reconstruct(record, "bm25", docs, chunks)
    assert ranking == [str(i) for i in range(100)] and state == "success" and not errors
    assert score(ranking, {"9": 1, "99": 1, "100": 1}) == {
        "ndcg@10": (1 / math.log2(11)) / (1 + 1 / math.log2(3) + .5),
        "recall@10": 1 / 3, "recall@100": 2 / 3}
    assert score(["same"], {"same": 1})["ndcg@10"] == 1
    assert all(value == 0 for value in score([], labels).values())
    failed = dict(record, state="failed")
    assert reconstruct(failed, "bm25", docs, chunks)[0] == []
    assert reconstruct(None, "bm25", docs, chunks)[1] == "unattempted"
    assert reconstruct({"state": "not_attempted"}, "bm25", docs, chunks)[1] == "unattempted"
    assert (score(["same"], {"same": 1})["ndcg@10"] + score([], {"same": 1})["ndcg@10"]) / 2 == .5
    record["raw"]["hits"].append({"chunk_id": "bad", "document_id": "0", "score": 1})
    assert reconstruct(record, "bm25", docs, chunks)[0:2] == ([], "failed")
    psr = {"state": "success", "raw": {"pages": [{"response": {"code": 0, "data": {"chunks": [
        {"id": "c0", "document_id": "0", "similarity": 1}]}}},
        {"response": {"code": 100, "data": {"chunks": []}}}], "returned_chunks": 1}}
    assert reconstruct(psr, "psr", docs, chunks)[0:2] == ([], "failed")
    assert percentile([4, 1, 3, 2], .5) == 2
    assert percentile(list(range(1, 21)), .95) == 19
    assert percentile([], .95) is None
    a = {"a": score(["same"], {"same": 1}), "b": score([], {"same": 1})}
    b = {"a": a["b"], "b": a["a"]}
    assert sum(r["ndcg@10"] for r in a.values()) == sum(r["ndcg@10"] for r in b.values())
    assert metric_differences(a, b)[0] == 1 and len(metric_differences(a, b)[1]) == 6
    assert term_change_summary([]) == {"term_changed_chunks": 0, "term_changed_query_count": 0,
        "term_changed_query_ids": [], "term_max_absolute_change": 0.0, "term_max_change_example": None}
    controller, old = "a" * 40, "b" * 40
    digest = "c" * 64
    run = {"code_sha": controller, "runtime": {"source_hashes": {p: digest for p in REQUIRED_RUNTIME_PATHS}},
           "runtime_sources": {p: {"source_commit": controller, "sha256": digest, "role": "controller"} for p in REQUIRED_RUNTIME_PATHS}}
    override = {"source_commit": old, "sha256": digest, "reason": "old ES score scale only"}
    run["runtime_sources"][ES_ADAPTER] = dict(override, role="intentional_override")
    protocol = {"controlled_v2": {"schema": "psr-beir-controlled-v2", "controller_code_sha": controller,
        "variants": {"baseline": {"runtime_overrides": {ES_ADAPTER: override}}}}}
    assert runtime_origins(run, protocol, "baseline")[ES_ADAPTER] == old
    run["runtime_sources"]["rag/nlp/query.py"]["source_commit"] = old
    try:
        runtime_origins(run, protocol, "baseline")
    except ValueError:
        pass
    else:
        raise AssertionError("undeclared non-ES override accepted")
    good = 'for route in order:\n    write_json(path.with_suffix(".attempt.json"), row)\n    started = time.perf_counter()\n    worker.call("query")\n'
    assert timing_source_check(good)["ordered"]
    bad = 'for route in order:\n    started = time.perf_counter()\n    write_json(path.with_suffix(".attempt.json"), row)\n    worker.call("query")\n'
    try:
        timing_source_check(bad)
    except ValueError:
        pass
    else:
        raise AssertionError("persistence inside timing accepted")
    nlp = {"redis_synonyms_state": "absent", "redis_synonyms_sha256": hashlib.sha256(b"ABSENT:kevin_synonyms").hexdigest(),
           "lexical_assets": {}, "lexical_asset_presence": {path: False for path in ("synonym.json", "term.freq", "ner.json")}}
    validate_nlp_states(nlp)
    nlp["redis_synonyms_state"] = "empty"
    nlp["redis_synonyms_sha256"] = hashlib.sha256(b"").hexdigest()
    validate_nlp_states(nlp)
    nlp["redis_synonyms_state"] = "absent"
    try:
        validate_nlp_states(nlp)
    except ValueError:
        pass
    else:
        raise AssertionError("Redis absence was conflated with empty bytes")
    nlp["redis_synonyms_state"] = "empty"
    nlp["lexical_assets"]["synonym.json"] = "c" * 64
    try:
        validate_nlp_states(nlp)
    except ValueError:
        pass
    else:
        raise AssertionError("a missing lexical file was assigned a fabricated digest")
    return {"status": "PASS", "cases": ["graded multiple positives", "duplicate documents",
            "rank 10 and 100 boundaries", "empty, failure and unattempted", "identical query/document IDs",
            "stable tied order", "invalid late mapping discards partial ranking", "PSR later-page failure",
            "nearest-rank percentiles", "equal aggregate with different per-query values",
            "empty term-change set", "declared ES source origin", "undeclared override rejected", "timing boundary",
            "Redis absent versus empty", "missing lexical asset cannot have a digest"]}


def verify(evidence, output):
    issues, source_hashes = [], {}

    def read(relative):
        path = evidence / relative
        source_hashes[relative] = sha(path)
        return load(path)

    corpus, queries = {}, {}
    for relative, target in [("data/corpus.jsonl", corpus), ("data/queries.jsonl", queries)]:
        source_hashes[relative] = sha(evidence / relative)
        for line in (evidence / relative).read_text().splitlines():
            row = json.loads(line, object_pairs_hook=unique_object)
            key = str(row["_id"])
            if key in target:
                issues.append(f"duplicate ID in {relative}: {key}")
            if not row.get("text", "").strip():
                issues.append(f"empty text in {relative}: {key}")
            target[key] = row
    labels, pairs = {}, set()
    source_hashes["data/qrels/test.tsv"] = sha(evidence / "data/qrels/test.tsv")
    with (evidence / "data/qrels/test.tsv").open() as handle:
        qrel_rows = list(csv.DictReader(handle, delimiter="\t"))
    for row in qrel_rows:
        query, doc, relevance = row["query-id"], row["corpus-id"], float(row["score"])
        if (query, doc) in pairs or query not in queries or doc not in corpus:
            issues.append(f"duplicate or dangling qrel {query}/{doc}")
        if not math.isfinite(relevance):
            issues.append(f"nonfinite qrel {query}/{doc}")
        pairs.add((query, doc))
        labels.setdefault(query, {})[doc] = relevance
    query_ids = sorted(labels)
    document_rows = read("document-map.json")
    doc_map = {str(row["document_id"]): str(row["scifact_id"]) for row in document_rows}
    chunk_data = read("chunk-map.json")
    chunk_map = {str(row["chunk_id"]): str(row["document_id"]) for row in chunk_data["chunks"]}
    if len(doc_map) != len(document_rows) or len(set(doc_map.values())) != len(doc_map):
        issues.append("document map is not bijective")
    if set(doc_map.values()) != set(corpus) or set(chunk_map.values()) != set(doc_map):
        issues.append("corpus/document/chunk coverage mismatch")
    if len(chunk_map) != len(chunk_data["chunks"]):
        issues.append("duplicate chunk IDs")
    for row in document_rows:
        doc = corpus[str(row["scifact_id"])]
        text = doc.get("title", "").strip() + "\n\n" + doc["text"].strip()
        if hashlib.sha256(text.encode()).hexdigest() != row["input_sha256"]:
            issues.append(f"document input hash mismatch {row['scifact_id']}")
    manifest = read("data-manifest.json")
    for relative, expected in manifest["file_sha256"].items():
        if source_hashes["data/" + relative] != expected:
            issues.append(f"manifest hash mismatch {relative}")
    source_hashes["data/scifact.zip"] = sha(evidence / "data/scifact.zip")
    if source_hashes["data/scifact.zip"] != manifest["archive_sha256"]:
        issues.append("archive checksum mismatch")
    with zipfile.ZipFile(evidence / "data/scifact.zip") as archive:
        for relative in manifest["file_sha256"]:
            if archive.read("scifact/" + relative) != (evidence / "data" / relative).read_bytes():
                issues.append(f"archive member mismatch {relative}")
    protocol = read("protocol.json")
    protocol_hash = source_hashes["protocol.json"]
    test_queries = read("data/test-queries.json")
    if [(r["query_id"], r["text"]) for r in test_queries] != [(q, queries[q]["text"].strip()) for q in query_ids]:
        issues.append("prepared test queries differ from official test IDs or text")
    if protocol["test_queries_sha256"] != source_hashes["data/test-queries.json"]:
        issues.append("protocol test query checksum differs")
    if protocol["data_manifest_sha256"] != source_hashes["data-manifest.json"]:
        issues.append("protocol data manifest checksum differs")
    runs, all_rows = {}, []
    for run_name in ("baseline", "candidate"):
        run = read(f"{run_name}/run.json")
        after = read(f"{run_name}/runtime-after.json")
        if run["runtime"] != after:
            issues.append(f"{run_name}: runtime changed between before and after")
        for name, field in [("protocol.json", "protocol_sha256"), ("document-map.json", "document_map_sha256"),
                            ("chunk-map.json", "chunk_map_sha256")]:
            if run[field] != source_hashes[name]:
                issues.append(f"{run_name}: {field} mismatch")
        records, attempts = {}, {}
        for route in ROUTES:
            for path in sorted((evidence / run_name / "raw" / route).glob("*.json")):
                relative = str(path.relative_to(evidence))
                rec = read(relative)
                key = (route, str(rec.get("query_id")))
                target = attempts if path.name.endswith(".attempt.json") else records
                if key in target:
                    issues.append(f"{run_name}: duplicate {'attempt' if target is attempts else 'terminal'} {key}")
                target[key] = rec
                if rec.get("route") != route or rec.get("run_id") != run_name or rec.get("protocol_sha256") != protocol_hash:
                    issues.append(f"{relative}: identity mismatch")
                if rec.get("attempt") != 1:
                    issues.append(f"{relative}: attempt differs from frozen one-attempt rule")
        expected_keys = {(route, query) for route in ROUTES for query in query_ids}
        missing = sorted(expected_keys - records.keys())
        extra = sorted(records.keys() - expected_keys)
        if missing or extra:
            issues.append(f"{run_name}: terminal coverage missing={missing}, extra={extra}")
        if set(attempts) != expected_keys:
            issues.append(f"{run_name}: attempt coverage mismatch")
        rows, route_summary, timeline = [], {}, []
        for route in ROUTES:
            for query in query_ids:
                rec = records.get((route, query))
                ranking, state, errors = reconstruct(rec, route, doc_map, chunk_map)
                if rec is None and (route, query) in attempts:
                    state = "failed"
                    errors.append("attempt marker without terminal response")
                issues.extend(f"{run_name}/{route}/{query}: {error}" for error in errors)
                elapsed = rec.get("elapsed_s") if rec else None
                if elapsed is not None and (not isinstance(elapsed, (int, float)) or not math.isfinite(elapsed) or elapsed < 0):
                    issues.append(f"{run_name}/{route}/{query}: invalid timing")
                    elapsed = None
                if rec:
                    if (route, query) not in attempts or attempts[(route, query)].get("started_at") != rec.get("started_at"):
                        issues.append(f"{run_name}/{route}/{query}: attempt/terminal timestamp mismatch")
                    try:
                        start = datetime.datetime.fromisoformat(rec["started_at"])
                        finish = datetime.datetime.fromisoformat(rec["finished_at"])
                        wall = (finish - start).total_seconds()
                        if wall < 0 or elapsed is None or abs(wall - elapsed) > .05:
                            issues.append(f"{run_name}/{route}/{query}: wall/monotonic duration mismatch")
                        timeline.append((start, finish, route, query))
                    except (KeyError, ValueError):
                        issues.append(f"{run_name}/{route}/{query}: missing timestamps")
                    if [str(d["document_id"]) for d in rec.get("documents", [])] != ranking:
                        issues.append(f"{run_name}/{route}/{query}: stored derived document ranking differs")
                row = {"run_id": run_name, "route": route, "query_id": query, "state": state,
                       "elapsed_s": elapsed, "returned_documents": len(ranking),
                       **score(ranking, labels[query]), "document_ids": ranking}
                rows.append(row)
            selected = [row for row in rows if row["route"] == route]
            durations = [row["elapsed_s"] for row in selected if row["elapsed_s"] is not None]
            states = collections.Counter(row["state"] for row in selected)
            route_summary[route] = {metric: sum(row[metric] for row in selected) / len(query_ids) for metric in METRICS}
            route_summary[route].update({"expected": len(query_ids), "attempted": sum((route, q) in attempts for q in query_ids),
                "states": dict(states), "failed": states["failed"], "unattempted": states["unattempted"],
                "failure_rate": states["failed"] / len(query_ids), "unattempted_rate": states["unattempted"] / len(query_ids),
                "latency_samples": len(durations), "p50_s": percentile(durations, .5), "p95_s": percentile(durations, .95),
                "fewer_than_100": sum(row["returned_documents"] < 100 for row in selected),
                "min_documents": min(row["returned_documents"] for row in selected),
                "max_documents": max(row["returned_documents"] for row in selected)})
            for group, members in [("success", {"success", "empty"}), ("failed", {"failed"})]:
                values = [r["elapsed_s"] for r in selected if r["state"] in members and r["elapsed_s"] is not None]
                route_summary[route][group + "_latency"] = {"samples": len(values), "p50_s": percentile(values, .5), "p95_s": percentile(values, .95)}
        timeline.sort()
        wanted_order = [(ROUTES[(j + offset) % 3], query) for j, query in enumerate(query_ids) for offset in range(3)]
        if [(r, q) for _, _, r, q in timeline] != wanted_order:
            issues.append(f"{run_name}: request order differs from protocol")
        if any(b[0] < a[1] for a, b in itertools.pairwise(timeline)):
            issues.append(f"{run_name}: requests overlap despite concurrency 1")
        runs[run_name] = {"code_sha": run["code_sha"], "unique_terminal_pairs": len(records),
            "unique_attempt_pairs": len(attempts), "missing": missing, "extra": extra, "routes": route_summary,
            "started_at": timeline[0][0].isoformat() if timeline else None,
            "finished_at": timeline[-1][1].isoformat() if timeline else None}
        all_rows.extend(rows)
    report = {"identity": IDENTITY, "tool_sha256": sha(__file__), "command": sys.argv,
        "generated_at": datetime.datetime.now(datetime.UTC).isoformat(), "self_test": self_test(),
        "data": {"corpus": len(corpus), "all_queries": len(queries), "test_queries": len(labels),
                 "qrel_rows": len(qrel_rows), "positive_pairs": sum(g > 0 for docs in labels.values() for g in docs.values()),
                 "chunks": len(chunk_map)}, "protocol_sha256": protocol_hash, "runs": runs,
        "issues": issues, "source_hashes": source_hashes}
    output.mkdir(parents=True, exist_ok=True)
    with (output / "independent-per-query-v2.csv").open("w", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(all_rows[0]))
        writer.writeheader()
        for row in all_rows:
            writer.writerow(dict(row, document_ids=json.dumps(row["document_ids"])))
    report["comparison"] = compare_engineering(evidence, all_rows, runs)
    report["metric_verdict"] = "PASS" if not issues and not report["comparison"]["differences"] else "FAIL"
    report["audit"] = audit(evidence, all_rows, protocol, queries, read, output)
    report["overall_verdict"] = "PENDING_INDEPENDENT_REVIEW" if report["metric_verdict"] == "PASS" and report["audit"]["automatic_checks"] == "PASS" else "PARTIAL"
    (output / "verification-v2.json").write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
    write_v2_report(report, output)
    print(json.dumps({key: report[key] for key in ("metric_verdict", "overall_verdict", "data", "runs", "issues", "comparison")}, indent=2))


def compare_engineering(evidence, rows, runs):
    differences, maxima, files = [], {}, {}
    for run_name, run in runs.items():
        actual = {(row["route"], row["query_id"]): row for row in rows if row["run_id"] == run_name}
        csv_path = evidence / run_name / "per-query.csv"
        summary_path = evidence / run_name / "summary.json"
        files[str(csv_path)] = sha(csv_path)
        files[str(summary_path)] = sha(summary_path)
        with csv_path.open() as handle:
            source_rows = list(csv.DictReader(handle))
        expected = {}
        for row in source_rows:
            key = (row["route"], row["query_id"])
            if key in expected:
                differences.append({"run": run_name, "key": key, "reason": "duplicate engineering row"})
            expected[key] = {**row, **{metric: float(row[metric]) for metric in METRICS}}
        maximum, metric_errors = metric_differences(expected, actual)
        differences.extend(dict(error, run=run_name) for error in metric_errors)
        per_field_maximum = {metric: 0.0 for metric in METRICS}
        timing_maximum = 0.0
        for key in expected.keys() & actual.keys():
            left, right = expected[key], actual[key]
            for metric in METRICS:
                per_field_maximum[metric] = max(per_field_maximum[metric], abs(left[metric] - right[metric]))
            for a, b in [(left["state"], right["state"]), (int(left["documents"]), right["returned_documents"])]:
                if a != b:
                    differences.append({"run": run_name, "key": key, "expected": a, "actual": b})
            source_elapsed = float(left["elapsed_s"]) if left["elapsed_s"] else None
            if source_elapsed is not None and right["elapsed_s"] is not None:
                delta = abs(source_elapsed - right["elapsed_s"])
                timing_maximum = max(timing_maximum, delta)
                if delta > TOLERANCE:
                    differences.append({"run": run_name, "key": key, "reason": "elapsed mismatch", "delta": delta})
            elif source_elapsed != right["elapsed_s"]:
                differences.append({"run": run_name, "key": key, "reason": "elapsed missing"})
        summary = load(summary_path)
        aggregate_maximum = 0.0
        aggregate_field_maximum = {}
        for route, computed in run["routes"].items():
            engineered = summary[route]
            checks = [(metric, computed[metric], engineered[metric]) for metric in METRICS]
            checks.extend((key, value, engineered[key]) for key, value in {
                "expected_queries": computed["expected"], "attempted_queries": computed["attempted"],
                "failure_count": computed["failed"], "failure_rate": computed["failure_rate"],
                "not_attempted": computed["unattempted"], "not_attempted_rate": computed["unattempted_rate"],
                "empty_results": computed["states"].get("empty", 0), "unknown_attempts": 0,
                "full_attempt_coverage": computed["attempted"] == computed["expected"],
                "latency_complete": computed["latency_samples"] == computed["attempted"]}.items())
            selected = [r for r in rows if r["run_id"] == run_name and r["route"] == route]
            for group, states in [("all", {"success", "empty", "failed"}), ("success", {"success", "empty"}), ("failed", {"failed"})]:
                durations = [r["elapsed_s"] for r in selected if r["state"] in states and r["elapsed_s"] is not None]
                fields = {"count": len(durations), "min": min(durations) if durations else None,
                          "avg": sum(durations) / len(durations) if durations else None,
                          "p50": percentile(durations, .5), "p90": percentile(durations, .9), "p95": percentile(durations, .95)}
                checks.extend((f"latency_{group}.{key}", value, engineered[f"latency_{group}"][key]) for key, value in fields.items())
            for field, value, reference in checks:
                delta = abs(value - reference) if value is not None and reference is not None else 0.0
                if (value is None) != (reference is None) or not math.isfinite(delta) or delta > TOLERANCE:
                    differences.append({"run": run_name, "route": route, "field": field,
                                        "independent": value, "engineering": reference})
                aggregate_maximum = max(aggregate_maximum, delta)
                aggregate_field_maximum[field] = max(aggregate_field_maximum.get(field, 0.0), delta)
        maxima[run_name] = {"per_query_metrics": maximum, "per_query_by_metric": per_field_maximum,
                            "per_query_elapsed_s": timing_maximum, "aggregates": aggregate_maximum,
                            "aggregate_by_field": aggregate_field_maximum}
    return {"threshold": TOLERANCE, "maximum_absolute_errors": maxima, "differences": differences, "file_sha256": files}


def write_v2_report(report, output):
    audit_result = report["audit"]
    lines = ["# Controlled v2 independent verification", "",
             f"Metric reconstruction: {report['metric_verdict']}. Overall: {report['overall_verdict']}.",
             f"Evaluator: {IDENTITY}. Tool SHA256: {report['tool_sha256']}.", "",
             "| Run | Route | nDCG@10 | Recall@10 | Recall@100 | p50 seconds | p95 seconds |",
             "|---|---|---:|---:|---:|---:|---:|"]
    for run_name, run in report["runs"].items():
        for route, values in run["routes"].items():
            lines.append(f"| {run_name} | {route} | {values['ndcg@10']} | {values['recall@10']} | {values['recall@100']} | {values['p50_s']} | {values['p95_s']} |")
    lines.extend(["", f"Changed shared-hit term scores: {audit_result['term_changed_chunks']} hits across {audit_result['term_changed_query_count']} queries.",
        f"Maximum term change: {audit_result['term_max_absolute_change']}. Example: {audit_result['term_max_change_example']}.",
        f"Maximum vector conversion error: {audit_result['vector_transform_max_absolute_error']}.",
        f"Changed returned sets: {audit_result['returned_chunk_set_changed_queries']} queries. A changed returned set alone is not a protocol violation.",
        "", "Preflight gate failures:", json.dumps(audit_result['preflight_gate_errors'], ensure_ascii=False, indent=2),
        "", "All numeric errors, source origins and coverage failures are retained in verification-v2.json.",
        "Automatic checks do not grant final independent acceptance. Serving fingerprints and sentinels are observational controls, not proof of resident weight identity.", ""])
    (output / "verification-v2.md").write_text("\n".join(lines))


def audit(evidence, rows, protocol, queries, read, output):
    repository = evidence.parents[2]
    source_checks, source_failures, request_errors, score_checks = [], [], [], {}
    runtime_files = {}
    documents = {(r["run_id"], r["route"], r["query_id"]): r for r in rows}
    raw_psr, raw_native = {}, {}
    warmup_checks = {}
    for run_name in ("baseline", "candidate"):
        run = read(f"{run_name}/run.json")
        commit = run["code_sha"]
        origins = runtime_origins(run, protocol, run_name)

        def source(path, runtime=False, origins=origins, commit=commit):
            origin = origins.get(path, commit) if runtime else commit
            return subprocess.check_output(["git", "-C", str(repository), "show", origin + ":" + path])

        for kind, mapping in [("controller", run["file_sha256"]), ("runtime", run["runtime"]["source_hashes"])]:
            for path, expected in mapping.items():
                path = "test/benchmark/" + path if kind == "controller" else path
                origin = origins[path] if kind == "runtime" else commit
                digest = hashlib.sha256(source(path, runtime=kind == "runtime")).hexdigest()
                item = {"run": run_name, "commit": origin, "controller_commit": commit, "path": path, "sha256": digest, "matches_record": digest == expected}
                source_checks.append(item)
                if digest != expected:
                    source_failures.append(item)
        worker_sha = hashlib.sha256(source("test/benchmark/beir_engine.py") + source("test/benchmark/http_client.py")).hexdigest()
        if worker_sha != run["runtime"]["worker_sha256"]:
            source_failures.append({"run": run_name, "field": "worker_sha256"})
        runtime_files[run_name] = {path: hashlib.sha256(source(path, runtime=True)).hexdigest() for path in [
            "rag/nlp/query.py", "rag/nlp/synonym.py", "rag/nlp/term_weight.py", "rag/svr/task_executor.py",
            "common/doc_store/es_conn_base.py", "test/benchmark/beir.py", "test/benchmark/beir_engine.py"]}
        tree = ast.parse(source("common/doc_store/es_conn_base.py", runtime=True))
        method = next(node for node in ast.walk(tree) if isinstance(node, ast.FunctionDef) and node.name == "get_scores")
        namespace = {}
        exec(compile(ast.Module(body=[method], type_ignores=[]), commit + ":get_scores", "exec"), namespace)  # noqa: S102 - Execute only the getter AST extracted from the hash-verified committed source.
        observed = namespace["get_scores"](None, {"hits": {"hits": [{"_id": "positive", "_score": .9},
            {"_id": "orthogonal", "_score": .5}, {"_id": "negative", "_score": .25}, {"_id": "missing", "_score": None}]}})
        expected_cosine = {"positive": .8, "orthogonal": 0., "negative": -.5, "missing": 0.}
        score_checks[run_name] = {"scope": "exact committed getter AST only, no imports or service calls",
            "observed": observed, "cosine_expectation_pass": all(abs(observed[k] - v) < 1e-12 for k, v in expected_cosine.items())}
        warm_paths = sorted((evidence / run_name / "warmup").glob("*.json"))
        warmed = []
        for path in warm_paths:
            warmed.extend(read(str(path.relative_to(evidence))))
        warmup_checks[run_name] = {"files": len(warm_paths), "calls": len(warmed),
            "route_text_pairs": [(r["route"], r["question"]) for r in warmed],
            "matches_protocol": [(r["route"], r["question"]) for r in warmed] == [(route, text) for route in ROUTES for text in protocol["execution"]["warmup"]]}
        dataset = run["dataset_id"]
        params = protocol["routes"]["psr"]["parameters"]
        filters = [{"term": {"kb_id": dataset}}, {"bool": {"must_not": [{"range": {"available_int": {"lt": 1}}}]}}]
        for route in ROUTES:
            for path in sorted((evidence / run_name / "raw" / route).glob("*.json")):
                if path.name.endswith(".attempt.json"):
                    continue
                row = read(str(path.relative_to(evidence)))
                query = row["query_id"]
                raw = row["raw"]
                valid = True
                if route == "psr":
                    pages = raw["pages"]
                    valid = len(pages) == 1 and raw["api_window_limit"] == 64 and raw["candidate_limit"] == 64
                    for page in pages:
                        expected = dict(params, dataset_ids=[dataset], page=1, page_size=64, question=queries[query]["text"].strip())
                        valid = valid and page["request"] == expected and page["response"]["code"] == 0
                        valid = valid and all(c["dataset_id"] == dataset for c in page["response"]["data"]["chunks"])
                    raw_psr[(run_name, query)] = pages[0]["response"]["data"]["chunks"]
                else:
                    request = raw["request_without_vector_values"]
                    valid = raw["parameters"] == params and raw["candidate_limit"] == 1024
                    if route == "vector":
                        valid = valid and request == {"knn": {"field": "q_384_vec", "filter": {"bool": {"filter": filters}},
                            "k": 1024, "num_candidates": 2048, "similarity": .2}}
                    else:
                        match = request["query"]["bool"]["must"][0]["match"]["content_ltks"]
                        valid = valid and set(match) == {"operator", "query"} and match["operator"] == "or"
                        valid = valid and request == {"_source": ["doc_id", "kb_id"], "query": {"bool": {"filter": filters,
                            "must": [{"match": {"content_ltks": match}}]}}, "size": 1024}
                    raw_native[(run_name, route, query)] = raw["hits"]
                if not valid:
                    request_errors.append({"run": run_name, "route": route, "query": query})
    query_ids = sorted(q for run, q in raw_psr if run == "baseline")
    term_changes, new_chunks, replay_mismatches, fusion_errors, returned_set_changes, content_changes = [], [], [], [], [], []
    vector_max_error, common_chunks = 0.0, 0
    for query in query_ids:
        before, after = raw_psr[("baseline", query)], raw_psr[("candidate", query)]
        by_id = {h["id"]: h for h in before}
        after_ids = {h["id"] for h in after}
        if set(by_id) != after_ids:
            returned_set_changes.append({"query_id": query, "baseline_only": sorted(set(by_id) - after_ids),
                                         "candidate_only": sorted(after_ids - set(by_id))})
        for h in after:
            if h["id"] not in by_id:
                new_chunks.append({"query_id": query, "chunk_id": h["id"], "document_id": h["document_id"]})
                continue
            old = by_id[h["id"]]
            if any(h.get(field) != old.get(field) for field in ("content", "content_ltks", "document_keyword", "important_keywords")):
                content_changes.append({"query_id": query, "chunk_id": h["id"]})
            common_chunks += 1
            vector_max_error = max(vector_max_error, abs(h["vector_similarity"] - (2 * old["vector_similarity"] - 1)))
            difference = h["term_similarity"] - old["term_similarity"]
            if abs(difference) > TOLERANCE:
                term_changes.append({"query_id": query, "chunk_id": h["id"], "document_id": h["document_id"],
                    "before": old["term_similarity"], "after": h["term_similarity"], "difference": difference})
        for run_name, chunks in [("baseline", before), ("candidate", after)]:
            for h in chunks:
                if abs(h["similarity"] - (.7 * h["term_similarity"] + .3 * h["vector_similarity"])) > TOLERANCE:
                    fusion_errors.append({"run": run_name, "query": query, "chunk": h["id"]})
        predicted = sorted(before, key=lambda h: .7 * h["term_similarity"] + .3 * (2 * h["vector_similarity"] - 1), reverse=True)
        predicted = [h["id"] for h in predicted if .7 * h["term_similarity"] + .3 * (2 * h["vector_similarity"] - 1) >= .2]
        if predicted != [h["id"] for h in after]:
            replay_mismatches.append(query)
    with (output / "term-similarity-differences-v2.csv").open("w", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=["query_id", "chunk_id", "document_id", "before", "after", "difference"])
        writer.writeheader()
        writer.writerows(term_changes)
    change_summary = {}
    for route in ROUTES:
        deltas = [documents[("candidate", route, q)]["ndcg@10"] - documents[("baseline", route, q)]["ndcg@10"] for q in query_ids]
        change_summary[route] = {"improved": sum(d > TOLERANCE for d in deltas), "regressed": sum(d < -TOLERANCE for d in deltas),
            "unchanged": sum(abs(d) <= TOLERANCE for d in deltas),
            "document_rankings_changed": sum(documents[("candidate", route, q)]["document_ids"] != documents[("baseline", route, q)]["document_ids"] for q in query_ids)}
        if route != "psr":
            change_summary[route]["native_hit_sequences_changed"] = sum(raw_native[("candidate", route, q)] != raw_native[("baseline", route, q)] for q in query_ids)
    proof_checks = {}
    for run_name in ("baseline", "candidate"):
        if not (evidence / f"{run_name}-cosine-proof.json").exists():
            proof_checks[run_name] = {"available": False, "limitation": "No extra non-test cosine probe; main raw responses still audited."}
            continue
        proof = read(f"{run_name}-cosine-proof.json")
        proof_checks[run_name] = {"observations": len(proof["observations"]),
            "source_matches_commit": proof["file_sha256"] == runtime_files[run_name]["common/doc_store/es_conn_base.py"],
            "max_error_from_recorded_cosine": max(abs(o["reported"] - o["independent_cosine"]) for o in proof["observations"]),
            "max_error_from_transformed_recorded_cosine": max(abs(o["reported"] - (1 + o["independent_cosine"]) / 2) for o in proof["observations"]),
            "limitation": "Stored observations omit original query/chunk vectors and full probe HTTP response; cosine itself cannot be independently recalculated from this file."}
    preflight_path = evidence / "preflight-verdict.json"
    gate_errors = []
    receipt = {}
    if not preflight_path.exists():
        gate_errors.append("independent preflight receipt missing")
    else:
        receipt = load(preflight_path)
        if receipt.get("protocol_sha256") != sha(evidence / "protocol.json") or receipt.get("automatic_checks") != "PASS":
            gate_errors.append("independent preflight receipt does not match this protocol or did not pass")
        if receipt.get("verdict") != "PASS" or receipt.get("evaluator_id") != IDENTITY:
            gate_errors.append("independent preflight not approved")
        if receipt.get("input_manifest_sha256") != sha(evidence / "preflight.json"):
            gate_errors.append("preflight input manifest changed after review")
        gate_time = datetime.datetime.fromisoformat(receipt.get("approved_at", receipt["generated_at"]))
        for run_name in ("baseline", "candidate"):
            for path in (evidence / run_name / "raw").rglob("*.attempt.json"):
                attempt = load(path)
                if datetime.datetime.fromisoformat(attempt["started_at"]) <= gate_time:
                    gate_errors.append(f"{run_name}: attempt predates independent gate")
                    break
    timing_checks = {}
    for run_name in ("baseline", "candidate"):
        run = read(f"{run_name}/run.json")
        preflight_manifest = read("preflight.json")
        expected_observation = preflight_manifest["variants"][run_name]
        if run["code_sha"] != preflight_manifest["code_sha"] or run["code_sha"] != receipt.get("controller_code_sha"):
            gate_errors.append(f"{run_name}: controller SHA differs from approved preflight")
        if run["runtime_sources"] != expected_observation["runtime_sources"]:
            gate_errors.append(f"{run_name}: runtime source declarations changed since preflight")
        if run["runtime"].get("nlp") != protocol.get("nlp"):
            gate_errors.append(f"{run_name}: runtime NLP fingerprint differs from protocol")
        if run["index_configuration"] != expected_observation["index_configuration"]:
            gate_errors.append(f"{run_name}: index configuration differs from preflight")
        try:
            controller = subprocess.check_output(["git", "-C", str(repository), "show", run["code_sha"] + ":test/benchmark/beir.py"])
            timing_checks[run_name] = timing_source_check(controller)
        except ValueError as exc:
            gate_errors.append(str(exc))
    automatic_pass = not source_failures and not request_errors and not term_changes and not fusion_errors and not gate_errors and vector_max_error <= TOLERANCE
    return {"source_checks": source_checks, "source_failures": source_failures, "additional_commit_file_hashes": runtime_files,
        "exact_getter_behavior_checks": score_checks,
        "request_parameter_errors": request_errors, "warmup": warmup_checks,
        "two_run_changes": change_summary, "cosine_proof_checks": proof_checks,
        "common_psr_chunks": common_chunks, "vector_transform_max_absolute_error": vector_max_error,
        "fusion_arithmetic_errors": fusion_errors, **term_change_summary(term_changes),
        "new_candidate_chunks": new_chunks, "new_returned_chunk_queries": len({r["query_id"] for r in new_chunks}),
        "returned_chunk_set_changes": returned_set_changes, "common_returned_content_changes": content_changes,
        "returned_chunk_set_changed_queries": len(returned_set_changes),
        "baseline_only_returned_chunk_count": sum(len(r["baseline_only"]) for r in returned_set_changes),
        "candidate_only_returned_chunk_count": len(new_chunks),
        "cosine_only_replay_mismatch_queries": replay_mismatches,
        "automatic_checks": "PASS" if automatic_pass else "FAIL", "fairness_verdict": "PENDING_INDEPENDENT_REVIEW",
        "preflight_gate_errors": gate_errors, "timing_source_checks": timing_checks,
        "single_cause_check": "PASS_FOR_SHARED_HITS" if not term_changes else "FAIL_TERM_SCORES_CHANGED",
        "runtime_identity": "Declared overrides are verified against their source_commit; other recorded files match controller SHA.",
        "model_index_identity": "Preflight fingerprints and sentinels are observational controls, not hardware proof of resident weights.",
        "scope": "Controlled v2 English SciFact document retrieval. No automatic overall approval; returned-set changes alone are not a violation."}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--preflight", action="store_true")
    parser.add_argument("--preflight-manifest", type=Path)
    parser.add_argument("--evidence", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.self_test:
        print(json.dumps(self_test(), indent=2))
    else:
        if not args.evidence or not args.output:
            parser.error("--evidence and --output are required")
        if args.preflight:
            preflight(args.evidence, args.output, args.preflight_manifest)
        else:
            verify(args.evidence, args.output)

