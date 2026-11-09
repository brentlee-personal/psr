"""Full SciFact evaluation against the existing PSR deployment."""
import argparse
import csv
import hashlib
import io
import json
import os
import shutil
import zipfile
from pathlib import Path

SOURCE_URL = "https://public.ukp.informatik.tu-darmstadt.de/thakur/BEIR/datasets/scifact.zip"
OFFICIAL_MD5 = "5f7d1de60b170fc8027bb7898e2efca1"


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    data = (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()
    temporary = path.with_name(path.name + ".partial")
    with temporary.open("wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(path)


def unique_records(raw, kind):
    records = {}
    for line in raw.decode("utf-8").splitlines():
        row = json.loads(line)
        identity = row["_id"]
        if not isinstance(identity, str) or not identity or identity in records:
            raise ValueError(f"{kind}: invalid or duplicate ID")
        records[identity] = row
    return records


def load_data(archive):
    with zipfile.ZipFile(archive) as zipped:
        wanted = {"corpus.jsonl", "queries.jsonl", "qrels/test.tsv"}
        members = {}
        for name in zipped.namelist():
            for suffix in wanted:
                if name == suffix or name == "scifact/" + suffix:
                    if suffix in members:
                        raise ValueError("Duplicate archive member")
                    members[suffix] = zipped.read(name)
        if set(members) != wanted:
            raise ValueError("Missing official dataset members")
    corpus = unique_records(members["corpus.jsonl"], "corpus")
    queries = unique_records(members["queries.jsonl"], "queries")
    qrels = {}
    pairs = set()
    reader = csv.DictReader(io.StringIO(members["qrels/test.tsv"].decode()), delimiter="\t")
    if reader.fieldnames != ["query-id", "corpus-id", "score"]:
        raise ValueError("Unexpected qrels header")
    for row in reader:
        qid, did = row["query-id"], row["corpus-id"]
        pair = (qid, did)
        relevance = int(row["score"])
        if pair in pairs or qid not in queries or did not in corpus or relevance not in (0, 1):
            raise ValueError("Invalid, duplicate, conflicting or dangling qrel")
        pairs.add(pair)
        qrels.setdefault(qid, {})[did] = relevance
    for qid, labels in qrels.items():
        if not queries[qid].get("text", "").strip() or not any(v > 0 for v in labels.values()):
            raise ValueError("Test query lacks text or positive relevance")
    for row in corpus.values():
        if not isinstance(row.get("text"), str) or not row["text"].strip():
            raise ValueError("Corpus document lacks abstract")
        if not isinstance(row.get("title", ""), str):
            raise TypeError("Corpus title is not text")
    if len(corpus) != 5183 or len(qrels) != 300:
        raise ValueError(f"Unexpected dataset size: corpus={len(corpus)}, test={len(qrels)}")
    return members, corpus, queries, qrels


def prepare_data(campaign, archive):
    import datetime
    campaign = Path(campaign)
    raw = Path(archive).read_bytes()
    if hashlib.md5(raw).hexdigest() != OFFICIAL_MD5:
        raise ValueError("Archive does not match the official BEIR checksum")
    members, corpus, queries, qrels = load_data(archive)
    target = campaign / "data"
    target.mkdir(parents=True, exist_ok=True)
    cached = target / "scifact.zip"
    if cached.exists() and sha256(cached.read_bytes()) != sha256(raw):
        raise ValueError("Existing campaign archive differs")
    if not cached.exists():
        shutil.copyfile(archive, cached)
    for name, content in members.items():
        destination = target / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.exists() and destination.read_bytes() != content:
            raise ValueError("Existing campaign raw member differs")
        destination.write_bytes(content)
    inputs = target / "inputs"
    inputs.mkdir(exist_ok=True)
    documents = []
    for did, row in sorted(corpus.items()):
        title, abstract = row.get("title", "").strip(), row["text"].strip()
        text = title + "\n\n" + abstract if title else abstract
        content = text.encode("utf-8")
        filename = sha256(did.encode()) + ".txt"
        path = inputs / filename
        if path.exists() and path.read_bytes() != content:
            raise ValueError("Existing input differs")
        path.write_bytes(content)
        documents.append({"scifact_id": did, "filename": filename, "input_sha256": sha256(content)})
    write_json(target / "documents.json", documents)
    write_json(target / "test-queries.json", [{"query_id": qid, "text": queries[qid]["text"]} for qid in sorted(qrels)])
    manifest = {
        "schema": "psr-beir-data-v1", "source_url": SOURCE_URL,
        "source_version": "BEIR SciFact official archive, identified by checksum",
        "acquisition": "verified existing public archive cache", "cache_source": str(archive),
        "archive_sha256": sha256(raw), "archive_md5": hashlib.md5(raw).hexdigest(),
        "archive_bytes": len(raw), "corpus_count": len(corpus), "all_queries_count": len(queries),
        "acquired_for_campaign_at": datetime.datetime.fromtimestamp(cached.stat().st_mtime, datetime.UTC).isoformat(),
        "source_cache_mtime": datetime.datetime.fromtimestamp(Path(archive).stat().st_mtime, datetime.UTC).isoformat(),
        "test_queries_count": len(qrels), "qrels_rows": sum(len(x) for x in qrels.values()),
        "positive_pairs": sum(v > 0 for x in qrels.values() for v in x.values()),
        "file_sha256": {name: sha256(value) for name, value in members.items()},
        "corpus_ids_sha256": sha256(("\n".join(sorted(corpus)) + "\n").encode()),
        "test_ids_sha256": sha256(("\n".join(sorted(qrels)) + "\n").encode()),
        "positive_documents_per_query": {qid: sum(v > 0 for v in qrels[qid].values()) for qid in sorted(qrels)},
        "documents_manifest_sha256": sha256((target / "documents.json").read_bytes()),
        "normalization": "strip title and abstract; title + two newlines + abstract; UTF-8",
        "gold_indexed": False,
    }
    write_json(campaign / "data-manifest.json", manifest)
    return manifest


class RemoteError(RuntimeError):
    def __init__(self, kind, message, http_status=None):
        super().__init__(kind + ": " + message)
        self.kind = kind
        self.http_status = http_status


class Worker:
    def __init__(self):
        import select
        import subprocess
        source = Path(__file__).with_name("beir_engine.py").read_text()
        helper = Path(__file__).with_name("http_client.py").read_text()
        digest = sha256((source + helper).encode())
        bootstrap = chr(10).join(["helper_namespace={}", "exec(" + repr(helper) + ", helper_namespace)",
                                  "engine_namespace={'__name__':'beir_engine'}", "exec(" + repr(source) + ", engine_namespace)",
                                  "engine_namespace['main'](helper_namespace['HttpClient'], " + repr(digest) + ")", ""])
        self.process = subprocess.Popen(
            ["docker", "--host", "unix:///Users/libin/.colima/default/docker.sock", "exec", "-i",
             "ragflow-psr-m05-ragflow-cpu-1", "python3", "-u", "-c", bootstrap],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
        )
        if not select.select([self.process.stdout], [], [], 120)[0]:
            self.close()
            raise TimeoutError("Worker initialization timed out")
        line = self.process.stdout.readline()
        if not line or json.loads(line).get("worker_sha256") != digest:
            self.close()
            raise RuntimeError("Worker initialization failed or identity differs")

    def call(self, action, **arguments):
        import select
        command = {"action": action, **arguments}
        self.process.stdin.write(json.dumps(command, ensure_ascii=False, allow_nan=False) + "\n")
        self.process.stdin.flush()
        if not select.select([self.process.stdout], [], [], 120)[0]:
            self.close()
            raise TimeoutError("Worker exceeded query budget; backend completion is unverified")
        line = self.process.stdout.readline()
        if not line:
            raise RuntimeError("Worker exited without a result")
        value = json.loads(line)
        if not value.get("ok"):
            raise RemoteError(value.get("error_type", "WorkerError"), value.get("error", ""), value.get("http_status"))
        return value["data"]

    def request_json(self, method, path, json_body=None, params=None):
        return self.call("api", method=method, path=path, body=json_body, params=params)

    def close(self):
        import subprocess
        if self.process.poll() is None:
            self.process.stdin.close()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.terminate()
                self.process.wait(timeout=5)

    def __enter__(self):
        return self

    def __exit__(self, *unused):
        self.close()


def preflight(campaign):
    with Worker() as worker:
        observed = worker.call("status")
    write_json(Path(campaign) / "runtime-binding.json", observed)
    print(json.dumps({k: v for k, v in observed.items() if k != "dataset_configs"}, ensure_ascii=False))

def all_documents(client, identity):
    from .dataset import list_documents
    documents = {}
    page = 1
    while True:
        value = list_documents(client, identity, {"page": page, "page_size": 100, "orderby": "name", "desc": False})
        rows = value.get("docs", [])
        if not rows:
            break
        for row in rows:
            if row["id"] in documents:
                raise ValueError("Duplicate document across pages")
            documents[row["id"]] = row
        if len(documents) >= value["total"]:
            break
        page += 1
    if len(documents) != value["total"]:
        raise ValueError("Document pagination coverage differs from total")
    return list(documents.values())


def prepare_index(campaign):
    from .dataset import create_dataset, parse_documents, wait_for_parse_done
    campaign = Path(campaign)
    manifest = json.loads((campaign / "data-manifest.json").read_text())
    inputs = json.loads((campaign / "data/documents.json").read_text())
    resources_path = campaign / "resources.json"
    name = "bre-218-scifact-full-20261109"
    with Worker() as worker:
        observed = worker.call("status")
        write_json(campaign / "runtime-binding.json", observed)
        if resources_path.exists():
            resources = json.loads(resources_path.read_text())
            if resources["archive_sha256"] != manifest["archive_sha256"] or resources["dataset_name"] != name:
                raise ValueError("Existing campaign identity differs")
            identity = resources["dataset_id"]
        else:
            if any(d["name"] == name for d in observed["dataset_configs"]):
                raise ValueError("Existing dataset has no campaign ownership receipt")
            config = {"name": name, "description": "BRE-218 full public SciFact; owner runtime-8d86dad8; " + manifest["archive_sha256"],
                      "language": "English", "permission": "me", "embedding_model": observed["model"],
                      "chunk_method": observed["chunk_method"], "parser_config": observed["parser_config"]}
            created = create_dataset(worker, name, config)
            identity = created["id"]
            resources = {"dataset_id": identity, "dataset_name": name, "archive_sha256": manifest["archive_sha256"],
                         "model": observed["model"], "parser_config": observed["parser_config"],
                         "owner": "runtime-8d86dad8", "source_config_only": True, "business_data_imported": False}
            write_json(resources_path, resources)
        expected_names = {d["filename"] for d in inputs}
        for offset in range(0, len(inputs), 64):
            current = all_documents(worker, identity)
            by_name = {d["name"]: d for d in current}
            if len(by_name) != len(current) or set(by_name) - expected_names:
                raise ValueError("Unexpected or duplicate document in isolated dataset")
            group = inputs[offset:offset + 64]
            missing = [d for d in group if d["filename"] not in by_name]
            if missing:
                payload = [{"filename": d["filename"], "input_sha256": d["input_sha256"],
                            "text": (campaign / "data/inputs" / d["filename"]).read_text()} for d in missing]
                value = worker.call("upload", dataset_id=identity, documents=payload)
                if value.get("code") != 0:
                    raise ValueError("Upload failed; reconcile existing documents before retry")
                rows = value["data"]
                if {r["name"] for r in rows} != {d["filename"] for d in missing}:
                    raise ValueError("Upload acknowledgement coverage differs")
                by_name.update({r["name"]: r for r in rows})
            selected = [by_name[d["filename"]] for d in group]
            pending = [d["id"] for d in selected if d.get("run") not in ("DONE", "RUNNING")]
            if pending:
                parse_documents(worker, identity, pending)
            wait_for_parse_done(worker, identity, [d["id"] for d in selected], 7200, 2)
            write_json(campaign / "index-progress.json", {"dataset_id": identity, "completed_documents": min(offset + 64, len(inputs)),
                                                        "expected_documents": len(inputs)})
            print(json.dumps({"phase": "index", "completed_documents": min(offset + 64, len(inputs)), "expected": len(inputs)}), flush=True)
        current = all_documents(worker, identity)
        by_name = {d["name"]: d for d in current}
        if set(by_name) != expected_names or any(d.get("run") != "DONE" for d in current):
            raise ValueError("Full document coverage or DONE state is incomplete")
        mapping = [{"scifact_id": d["scifact_id"], "document_id": by_name[d["filename"]]["id"],
                    "filename": d["filename"], "input_sha256": d["input_sha256"]} for d in inputs]
        write_json(campaign / "document-map.json", mapping)
        chunks = worker.call("mapping", dataset_id=identity)
        document_ids = {d["document_id"] for d in mapping}
        if {d["document_id"] for d in chunks["chunks"]} != document_ids:
            raise ValueError("Chunk-to-document full coverage differs")
        if chunks["invalid_vectors"] or len(chunks["vector_dimensions"]) != 1:
            raise ValueError("Missing, non-finite or inconsistent indexed vectors")
        write_json(campaign / "chunk-map.json", chunks)
        resources.update({"documents": len(mapping), "chunks": len(chunks["chunks"]), "index_name": chunks["index_name"],
                          "vector_dimensions": chunks["vector_dimensions"], "complete": True})
        write_json(resources_path, resources)
        print(json.dumps({"phase": "index-complete", **resources}), flush=True)

def freeze_protocol(campaign, worker):
    campaign = Path(campaign)
    resources = json.loads((campaign / "resources.json").read_text())
    if not resources.get("complete"):
        raise ValueError("Full index coverage is not verified")
    observed = worker.call("status")
    defaults = worker.call("defaults")
    configuration = worker.call("index_configuration", dataset_id=resources["dataset_id"])
    configuration.pop("index_name")
    background_chunks = configuration.pop("index_total_chunks") - configuration.pop("dataset_chunks")
    if configuration["backend_version"] != "8.11.3":
        raise ValueError("Backend version changed from verified deployment")
    vector_fields = [v for k, v in configuration["fields"].items() if k.startswith("q_")]
    if len(vector_fields) != 1 or vector_fields[0].get("similarity") != "cosine":
        raise ValueError("Current vector similarity is not the verified cosine configuration")
    protocol = {
        "schema": "psr-beir-protocol-v1", "dataset": "SciFact",
        "data_manifest_sha256": sha256((campaign / "data-manifest.json").read_bytes()),
        "test_queries_sha256": sha256((campaign / "data/test-queries.json").read_bytes()),
        "model": json.loads((campaign / "model-binding.json").read_text()),
        "embedding_model": observed["model"], "parser_config": resources["parser_config"],
        "backend_index_configuration": configuration,
        "isolation": {"candidate_namespace": "dataset kb_id filter", "other_chunks_in_physical_index": background_chunks,
                      "bm25_statistics_scope": "physical tenant index, including the recorded other chunks"},
        "routes": {
            "bm25": {"algorithm": "ES BM25 only", "field": "content_ltks", "operator": "or",
                     "candidate_depth": defaults["knn_top_k"], "query_processing": "existing rag_tokenizer.tokenize", "rank_features": False},
            "vector": {"algorithm": "ES kNN only", "encoder": "current LLMBundle.encode_queries", "distance": "cosine",
                       "candidate_depth": defaults["knn_top_k"], "num_candidates": defaults["knn_num_candidates"],
                       "threshold": defaults["similarity_threshold"], "lexical_filter": False, "rank_features": False},
            "psr": {"algorithm": "deployed PSR via authenticated HTTP", "parameters": defaults,
                    "http_call_location": "verified PSR container loopback:9380, through the worker"},
        },
        "ranking": "first occurrence of each SciFact document in actual backend chunk order; retain first 100; preserve backend ties",
        "metrics": {"names": ["ndcg@10", "recall@10", "recall@100"], "scorer": "ranx==0.3.20",
                    "gain": "relevance", "discount": "log2(rank+1)", "positive": "relevance>0", "empty_or_failed": "zero",
                    "denominator": "all official test query IDs", "identical_query_and_document_ids": "retained",
                    "evaluation_order_score": "101-rank; separate from original retrieval scores"},
        "execution": {"concurrency": 1, "order": "query ID string ascending; rotate route start by query ordinal",
                      "warmup": ["stem cell differentiation", "protein expression", "clinical trial results"],
                      "warmup_scored": False, "max_attempts": 1, "connect_timeout_s": 5, "read_timeout_s": 60,
                      "query_deadline_s": 120, "cache_state": "process warm; index cache uncontrolled",
                      "timing": "host dispatch through response validation and document aggregation; exclude startup, warmup, indexing, scoring and persistence",
                      "percentile": "nearest-rank ceil(p*n)"},
    }
    path = campaign / "protocol.json"
    if path.exists() and json.loads(path.read_text()) != protocol:
        raise ValueError("Frozen protocol differs; do not overwrite or combine runs")
    if not path.exists():
        write_json(path, protocol)
    return protocol


def primary_score(records, qrels):
    import subprocess
    sources = {name: Path(__file__).with_name(name + ".py").read_text() for name in ("metrics", "beir_metrics")}
    launcher = chr(10).join([
        "import json,sys,types",
        "package=types.ModuleType('benchmark'); package.__path__=[]; sys.modules['benchmark']=package",
        "sources=" + repr(sources),
        "for name,source in sources.items():",
        "    module=types.ModuleType('benchmark.'+name); module.__package__='benchmark'; sys.modules[module.__name__]=module; exec(compile(source,name+'.py','exec'),module.__dict__)",
        "payload=json.load(sys.stdin)",
        "print(json.dumps(module.score_records(payload['records'],payload['qrels']),allow_nan=False))",
    ])
    result = subprocess.run(
        ["docker", "--host", "unix:///Users/libin/.colima/default/docker.sock", "exec", "-i",
         "ragflow-psr-m05-ragflow-cpu-1", "python3", "-c", launcher],
        input=json.dumps({"records": records, "qrels": qrels}, ensure_ascii=False), text=True, capture_output=True, timeout=120, check=False,
    )
    if result.returncode:
        raise RuntimeError("Primary scorer failed: " + result.stderr[-1200:])
    return json.loads(result.stdout)


def save_scoring(campaign, run_directory, records):
    import csv
    _, _, _, qrels = load_data(Path(campaign) / "data/scifact.zip")
    scored = primary_score(records, qrels)
    write_json(Path(run_directory) / "summary.json", scored["summary"])
    stream = io.StringIO()
    writer = csv.DictWriter(stream, fieldnames=list(scored["per_query"][0]))
    writer.writeheader()
    writer.writerows(scored["per_query"])
    (Path(run_directory) / "per-query.csv").write_text(stream.getvalue())
    (Path(run_directory) / "results.jsonl").write_text("".join(json.dumps(row, ensure_ascii=False, allow_nan=False) + "\n" for row in records))
    print(json.dumps({"phase": "scored", "run": Path(run_directory).name, "summary": scored["summary"]}), flush=True)
    return scored

def source_identity():
    import subprocess
    root = Path(__file__).resolve().parents[2]
    revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
    dirty = subprocess.check_output(["git", "status", "--porcelain"], cwd=root, text=True).strip()
    if dirty:
        raise ValueError("Formal queries require committed controller source")
    files = ["beir.py", "beir_engine.py", "beir_metrics.py", "http_client.py", "dataset.py", "metrics.py"]
    return {"code_sha": revision, "file_sha256": {name: sha256(Path(__file__).with_name(name).read_bytes()) for name in files},
            "controller_python": __import__("sys").version.split()[0]}


def existing_result(path, run_id, route, query_id, protocol_sha):
    path = Path(path)
    attempt = path.with_suffix(".attempt.json")
    if path.exists():
        row = json.loads(path.read_text())
    elif attempt.exists():
        row = json.loads(attempt.read_text())
        row.update(state="failed", documents=[], elapsed_s=None, attempt_state="unknown",
                   error={"type": "InterruptedAttempt", "message": "Attempt marker exists without a durable response; do not retry or invent elapsed time"})
        write_json(path, row)
    else:
        return None
    expected = {"run_id": run_id, "route": route, "query_id": query_id, "protocol_sha256": protocol_sha}
    if any(row.get(key) != value for key, value in expected.items()):
        raise ValueError("Saved record identity differs")
    return row

def run_evaluation(campaign, run_id):
    import datetime
    import re
    import time

    from .beir_metrics import ROUTES, aggregate_documents
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,63}", run_id):
        raise ValueError("Invalid run ID")
    campaign = Path(campaign)
    identity = source_identity()
    resources = json.loads((campaign / "resources.json").read_text())
    queries = json.loads((campaign / "data/test-queries.json").read_text())
    documents = json.loads((campaign / "document-map.json").read_text())
    document_map = {d["document_id"]: d["scifact_id"] for d in documents}
    directory = campaign / run_id
    directory.mkdir(exist_ok=True)
    if (directory / "summary.json").exists():
        raise ValueError("Run already finalized; preserve it and use a new run ID")
    lock = directory / "write.lock"
    handle = os.open(lock, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    os.write(handle, str(os.getpid()).encode())
    os.close(handle)
    records = []
    blocked = False
    try:
        with Worker() as worker:
            protocol = freeze_protocol(campaign, worker)
            protocol_sha = sha256((campaign / "protocol.json").read_bytes())
            runtime = worker.call("status")
            settings = worker.call("index_configuration", dataset_id=resources["dataset_id"])
            binding = {"schema": "psr-beir-run-v1", **identity, "protocol_sha256": protocol_sha,
                       "dataset_id": resources["dataset_id"], "runtime": runtime,
                       "document_map_sha256": sha256((campaign / "document-map.json").read_bytes()),
                       "chunk_map_sha256": sha256((campaign / "chunk-map.json").read_bytes()),
                       "index_configuration": settings, "route_transport": "host RPC; PSR uses real authenticated HTTP inside the verified container"}
            run_path = directory / "run.json"
            if run_path.exists() and json.loads(run_path.read_text()) != binding:
                raise ValueError("Resume identity differs from the recorded run")
            if not run_path.exists():
                write_json(run_path, binding)
            parameters = protocol["routes"]["psr"]["parameters"]
            warmups = []
            for route in ROUTES:
                for question in protocol["execution"]["warmup"]:
                    warm_started = time.perf_counter()
                    reply = worker.call("query", dataset_id=resources["dataset_id"], route=route,
                                        question=question, parameters=parameters)
                    if reply.get("error_type"):
                        raise ValueError("Warmup API failure: " + reply["error_type"])
                    aggregate_documents(reply["hits"], document_map)
                    warmups.append({"route": route, "question": question, "elapsed_s": time.perf_counter() - warm_started,
                                    "returned_chunks": len(reply["hits"]), "raw": reply})
            write_json(directory / "warmup" / (str(time.time_ns()) + ".json"), warmups)
            for ordinal, query in enumerate(queries):
                order = ROUTES[ordinal % 3:] + ROUTES[:ordinal % 3]
                for route in order:
                    raw_path = directory / "raw" / route / (sha256(query["query_id"].encode()) + ".json")
                    row = existing_result(raw_path, run_id, route, query["query_id"], protocol_sha)
                    if row is not None:
                        records.append(row)
                        blocked = blocked or row.get("attempt_state") == "unknown"
                        continue
                    row = {"run_id": run_id, "route": route, "query_id": query["query_id"],
                           "protocol_sha256": protocol_sha, "attempt": 1, "documents": [],
                           "started_at": datetime.datetime.now(datetime.UTC).isoformat()}
                    started = time.perf_counter()
                    if blocked:
                        row.update(state="not_attempted", attempt=0, elapsed_s=None,
                                   error={"type": "TransportBlocked", "message": "Backend completion or authorization is not verified"})
                    else:
                        write_json(raw_path.with_suffix(".attempt.json"), {**row, "state": "in_flight"})
                        try:
                            reply = worker.call("query", dataset_id=resources["dataset_id"], route=route,
                                                question=query["text"], parameters=parameters)
                            row["raw"] = reply
                            if reply.get("error_type"):
                                row["state"] = "failed"
                                row["error"] = {"type": reply["error_type"], "message": reply.get("error")}
                            else:
                                row["documents"] = aggregate_documents(reply["hits"], document_map)
                                row["state"] = "success" if row["documents"] else "empty"
                        except (ValueError, KeyError, TypeError, OSError, RuntimeError) as exc:
                            row.update(state="failed", documents=[], error={"type": type(exc).__name__, "message": str(exc)[:1200]})
                            blocked = isinstance(exc, (TimeoutError, BrokenPipeError)) or (isinstance(exc, RemoteError) and ("Timeout" in exc.kind or exc.http_status in (401, 403)))
                        row["elapsed_s"] = time.perf_counter() - started
                    row["finished_at"] = datetime.datetime.now(datetime.UTC).isoformat()
                    row["returned_documents"] = len(row["documents"])
                    row["returned_fewer_than_100"] = len(row["documents"]) < 100
                    write_json(raw_path, row)
                    records.append(row)
                if (ordinal + 1) % 10 == 0:
                    print(json.dumps({"phase": "queries", "run": run_id, "queries": ordinal + 1, "records": len(records)}), flush=True)
            if not blocked:
                after = worker.call("status")
                if after["source_hashes"] != runtime["source_hashes"] or after["worker_sha256"] != runtime["worker_sha256"]:
                    raise ValueError("Runtime source changed during the run")
                write_json(directory / "runtime-after.json", after)
        if len(records) != len(queries) * 3:
            raise ValueError("Run did not cover all three paths and all test queries")
        save_scoring(campaign, directory, records)
    finally:
        lock.unlink()

def report(campaign):
    campaign = Path(campaign)
    data = json.loads((campaign / "data-manifest.json").read_text())
    resources = json.loads((campaign / "resources.json").read_text())
    protocol = json.loads((campaign / "protocol.json").read_text())
    lines = ["# BRE-218 SciFact 全量检索结果", "",
             "仅评价英文科学文献检索。不设分数门槛，不代表回答质量、中文业务或生产验收。", "",
             f"语料 {data['corpus_count']} 篇，测试 {data['test_queries_count']} 题，正相关标注 {data['positive_pairs']} 对。",
             f"原包 SHA256：{data['archive_sha256']}。", f"数据集 ID：{resources['dataset_id']}；索引块数：{resources['chunks']}。",
             f"协议 SHA256：{sha256((campaign / 'protocol.json').read_bytes())}。", "",
             "BM25 使用现有租户物理索引的统计量；候选严格限制为本轮独立评测数据集。",
             f"该物理索引中的其他既有块数为 {protocol['isolation']['other_chunks_in_physical_index']}，未作为候选返回。",
             "PSR 在已核 MS 容器中通过正常鉴权真实 HTTP 调用。纯基线使用同一索引语料与当前 embedding 服务。", "",
             "进程已预热，索引缓存未受控，并发为 1。计时包含宿主 RPC、编码、检索、重排、分页和文档聚合。",
             "原生路径与 PSR HTTP 开销不同，耗时差不能直接解释为引擎速度差。", "",
             "| 运行 | 路径 | nDCG@10 | Recall@10 | Recall@100 | 失败 | 未尝试 | p50 ms | p95 ms |",
             "|---|---|---:|---:|---:|---:|---:|---:|---:|"]
    complete = True
    found = False
    for directory in sorted(campaign.iterdir()):
        if not directory.is_dir() or not (directory / "summary.json").exists():
            continue
        found = True
        summary = json.loads((directory / "summary.json").read_text())
        binding = json.loads((directory / "run.json").read_text())
        for route in ("bm25", "vector", "psr"):
            row = summary[route]
            complete = complete and row["full_attempt_coverage"]
            p50, p95 = row["latency_all"]["p50"], row["latency_all"]["p95"]
            t50 = f"{p50 * 1000:.3f}" if p50 is not None else "unknown"
            t95 = f"{p95 * 1000:.3f}" if p95 is not None else "unknown"
            lines.append(f"| {directory.name} | {route} | {row['ndcg@10']:.6f} | {row['recall@10']:.6f} | {row['recall@100']:.6f} | {row['failure_count']} | {row['not_attempted']} | {t50} | {t95} |")
        lines.extend(["", f"{directory.name} 被测控制器 SHA：{binding['code_sha']}。", ""])
    if not found:
        raise ValueError("No full-run scoring artifact exists")
    lines.extend(["## 验收状态", "", f"全部查询已尝试：{complete}。",
                  "独立复算以 verification.json 的实际记录为准，工程自报分不代表独立验收。",
                  "代码推送、实际修复加载和 Linear/MLflow/Notion 同步分别以各自读回回执为准。",
                  "原始结果、尝试标记、排名、失败和逐题指标均保留在本报告同目录。", ""])
    (campaign / "report.md").write_text("\n".join(lines))
    print(str(campaign / "report.md"))

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["prepare", "preflight", "run", "report"])
    parser.add_argument("--campaign-dir", type=Path, required=True)
    parser.add_argument("--archive", type=Path)
    parser.add_argument("--run-id", default="baseline")
    args = parser.parse_args()
    if args.command == "preflight":
        preflight(args.campaign_dir)
        return
    if args.command == "run":
        run_evaluation(args.campaign_dir, args.run_id)
        return
    if args.command == "report":
        report(args.campaign_dir)
        return
    if args.archive is None:
        parser.error("prepare requires --archive")
    manifest = prepare_data(args.campaign_dir, args.archive)
    print(json.dumps({key: value for key, value in manifest.items() if key != "positive_documents_per_query"}, ensure_ascii=False))
    prepare_index(args.campaign_dir)


if __name__ == "__main__":
    main()
