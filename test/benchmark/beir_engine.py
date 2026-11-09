"""Private JSON-lines worker; credentials never cross the process boundary."""
import contextlib
import hashlib
import io
import json
import logging
import math
import os
import re
import sys
import time
from pathlib import Path

CLIENT = None
TOKEN = None
SETTINGS = None
DATASETS = {}
MODELS = {}
WORKER_SHA256 = ""


def initialize(client_class):
    global CLIENT, TOKEN, SETTINGS
    logging.disable(logging.CRITICAL)
    os.environ["NO_PROXY"] = "127.0.0.1,localhost"
    from common import settings
    settings.init_settings()
    from api.db.db_models import DB, APIToken
    with DB.connection_context():
        rows = list(APIToken.select().where(APIToken.tenant_id == "994bfc7cd97511f19a1e1b399c94b5d4"))
    if len(rows) != 1:
        raise ValueError("Expected one existing authorized tenant API token")
    TOKEN = rows[0].token
    CLIENT = client_class("http://127.0.0.1:9380", api_key=TOKEN, connect_timeout=5, read_timeout=60)
    SETTINGS = settings


def request(method, path, body=None, params=None, files=None):
    response = CLIENT.request(method, path, json_body=body, params=params, files=files)
    try:
        value = response.json()
        if response.status_code != 200 or not isinstance(value, dict):
            error = ValueError(f"HTTP response rejected: {response.status_code}")
            error.http_status = response.status_code
            raise error
        return value
    finally:
        response.close()


def dataset(identity):
    if identity not in DATASETS:
        value = request("GET", f"/datasets/{identity}")
        config = value.get("data")
        if value.get("code") != 0 or not isinstance(config, dict) or not config.get("name", "").startswith("bre-218-scifact-"):
            raise ValueError("Dataset is outside this evaluation namespace")
        DATASETS[identity] = config
    return DATASETS[identity]


def api(command):
    method, path = command["method"], command["path"]
    body = command.get("body")
    if path == "/datasets":
        if method == "POST" and not (body or {}).get("name", "").startswith("bre-218-scifact-"):
            raise ValueError("Only an isolated evaluation dataset may be created")
        if method not in ("GET", "POST"):
            raise ValueError("Dataset mutation is not allowed")
    else:
        match = re.fullmatch(r"/datasets/([a-f0-9]+)(?:/(.*))?", path)
        if not match:
            raise ValueError("Unsupported management endpoint")
        dataset(match[1])
        suffix = match[2] or ""
        if method != "GET" and not (method == "POST" and suffix in ("chunks", "documents/parse")):
            raise ValueError("Unsupported evaluation mutation")
    return request(method, path, body=body, params=command.get("params"))


def status():
    value = request("GET", "/datasets")
    if value.get("code") != 0:
        raise ValueError("Existing API authentication failed")
    configs = value["data"]
    reference = [d for d in configs if d.get("embedding_model") == "BAAI/bge-small-en-v1.5@Builtin"]
    if not reference:
        raise ValueError("Current embedding configuration is not identifiable")
    source = reference[0]
    paths = ["api/apps/restful_apis/chunk_api.py", "rag/nlp/search.py", "rag/utils/es_conn.py", "common/doc_store/es_conn_base.py", "rag/llm/embedding_model.py", "api/db/services/llm_service.py",
             "rag/nlp/query.py", "rag/nlp/term_weight.py", "rag/nlp/synonym.py", "rag/nlp/rag_tokenizer.py"]
    return {
        "http_status": 200, "business_code": value["code"], "backend": SETTINGS.DOC_ENGINE,
        "dataset_configs": [{key: d.get(key) for key in ("id", "name", "embedding_model", "chunk_method", "parser_config")} for d in configs],
        "model": source["embedding_model"],
        "parser_config": source["parser_config"], "chunk_method": source["chunk_method"],
        "source_hashes": {p: hashlib.sha256((Path("/ragflow") / p).read_bytes()).hexdigest() for p in paths},
        "worker_sha256": WORKER_SHA256,
        "nlp": nlp_identity(),
    }


def nlp_identity():
    import importlib.metadata

    import infinity.rag_tokenizer
    from nltk.corpus import wordnet
    wordnet.ensure_loaded()
    pointer = wordnet.root
    archive = getattr(getattr(pointer, "zipfile", None), "filename", None)
    paths = [Path(archive)] if archive else sorted(p for p in Path(str(pointer)).rglob("*") if p.is_file())
    assets = {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in paths}
    query = SETTINGS.retriever.qryr
    raw = query.syn.redis.get("kevin_synonyms") if query.syn.redis else None
    if isinstance(raw, str):
        raw = raw.encode()
    lexical = {name: Path("/ragflow/rag/res") / name for name in ("synonym.json", "term.freq", "ner.json")}
    return {"python_hash_seed": os.environ.get("PYTHONHASHSEED"),
            "effective_synonyms_sha256": hashlib.sha256(json.dumps(query.syn.dictionary, sort_keys=True, ensure_ascii=False).encode()).hexdigest(),
            "redis_synonyms_sha256": hashlib.sha256(raw if raw is not None else b"ABSENT:kevin_synonyms").hexdigest(),
            "redis_synonyms_state": "absent" if raw is None else "empty" if not raw else "present",
            "wordnet_version": wordnet.get_version(), "wordnet_assets": assets,
            "lexical_assets": {name: hashlib.sha256(p.read_bytes()).hexdigest() for name, p in lexical.items() if p.is_file()},
            "lexical_asset_presence": {name: p.is_file() for name, p in lexical.items()},
            "infinity_tokenizer_source_sha256": hashlib.sha256(Path(infinity.rag_tokenizer.__file__).read_bytes()).hexdigest(),
            "infinity_sdk_version": importlib.metadata.version("infinity-sdk")}


def upload(command):
    identity = command["dataset_id"]
    dataset(identity)
    documents = command["documents"]
    files = []
    for doc in documents:
        if not re.fullmatch(r"[a-f0-9]{64}[.]txt", doc["filename"]):
            raise ValueError("Invalid public document filename")
        content = doc["text"].encode("utf-8")
        if hashlib.sha256(content).hexdigest() != doc["input_sha256"]:
            raise ValueError("Upload input checksum differs")
        files.append(("file", (doc["filename"], content, "text/plain")))
    return request("POST", f"/datasets/{identity}/documents", files=files)


def mapping(command):
    from elasticsearch.helpers import scan

    from rag.nlp.search import index_name
    identity = command["dataset_id"]
    dataset(identity)
    from api.db.db_models import DB, Knowledgebase
    with DB.connection_context():
        kb = Knowledgebase.get_by_id(identity)
    index = index_name(kb.tenant_id)
    if SETTINGS.DOC_ENGINE != "elasticsearch":
        raise ValueError("Live backend is not the verified Elasticsearch backend")
    rows = scan(SETTINGS.docStoreConn.es, index=index,
                query={"query": {"term": {"kb_id": identity}},
                       "_source": ["id", "doc_id", "content_with_weight", "available_int", "q_*_vec"]},
                request_timeout=60)
    chunks, dimensions, invalid, vector_rows = [], set(), [], []
    for hit in rows:
        source = hit["_source"]
        fields = [key for key in source if key.startswith("q_") and key.endswith("_vec")]
        vectors = [source[key] for key in fields]
        valid = len(vectors) == 1 and bool(vectors[0]) and all(isinstance(v, (int, float)) and math.isfinite(v) for v in vectors[0])
        if not valid or source.get("available_int", 1) < 1:
            invalid.append(hit["_id"])
        if valid:
            dimensions.add(len(vectors[0]))
            vector_rows.append((hit["_id"], vectors[0]))
        chunks.append({"chunk_id": hit["_id"], "document_id": source["doc_id"],
                       "content_sha256": hashlib.sha256(source["content_with_weight"].encode()).hexdigest(),
                       "content_chars": len(source["content_with_weight"]), "available": source.get("available_int", 1)})
    return {"index_name": index, "dataset_id": identity, "chunks": sorted(chunks, key=lambda d: d["chunk_id"]),
            "vector_dimensions": sorted(dimensions), "invalid_vectors": invalid,
            "vector_values_sha256": hashlib.sha256(json.dumps(sorted(vector_rows), separators=(",", ":"), allow_nan=False).encode()).hexdigest()}

def runtime_defaults():
    import ast
    tree = ast.parse(Path("/ragflow/api/apps/restful_apis/chunk_api.py").read_text())
    wanted = {"similarity_threshold", "vector_similarity_weight", "rerank_candidates_count", "keyword", "use_kg", "toc_enhance", "cross_languages", "include_knowledge_compilation"}
    values = {}
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute) or node.func.attr != "get" or len(node.args) < 2:
            continue
        key = node.args[0]
        if isinstance(key, ast.Constant) and key.value in wanted:
            values[key.value] = ast.literal_eval(node.args[1])
        if isinstance(key, ast.Name) and key.id == "knn_top_k_parameter":
            values["knn_top_k"] = ast.literal_eval(node.args[1])
    if wanted - values.keys() or "knn_top_k" not in values:
        raise ValueError("Runtime API defaults cannot be bound")
    values["knn_num_candidates"] = max(2048, values["knn_top_k"])
    values["rerank_id"] = None
    values["highlight"] = False
    return values


def current_model(identity):
    if identity not in MODELS:
        from api.db.db_models import DB, Knowledgebase
        from api.db.joint_services.tenant_model_service import resolve_model_config
        from api.db.services.llm_service import LLMBundle
        from common.constants import LLMType
        with DB.connection_context():
            kb = Knowledgebase.get_by_id(identity)
            config = resolve_model_config(kb.tenant_id, LLMType.EMBEDDING, kb.embd_id)
        MODELS[identity] = LLMBundle(kb.tenant_id, config, lang="English")
    return MODELS[identity]


def model_sentinels(command):
    model = current_model(command["dataset_id"])
    result = {}
    for text in ("stem cell differentiation", "protein expression", "clinical trial results"):
        vector, _ = model.encode_queries(text)
        values = vector.tolist()
        if len(values) != 384 or not all(math.isfinite(v) for v in values):
            raise ValueError("Model sentinel embedding is invalid")
        result[text] = hashlib.sha256(json.dumps(values, separators=(",", ":")).encode()).hexdigest()
    return result


def native_query(command):
    from api.db.db_models import DB, Knowledgebase
    from rag.nlp import rag_tokenizer
    from rag.nlp.search import index_name
    identity = command["dataset_id"]
    dataset(identity)
    with DB.connection_context():
        kb = Knowledgebase.get_by_id(identity)
    index = index_name(kb.tenant_id)
    params = command["parameters"]
    filters = [{"term": {"kb_id": identity}}, {"bool": {"must_not": [{"range": {"available_int": {"lt": 1}}}]}}]
    body = {"size": params["knn_top_k"], "_source": ["doc_id", "kb_id"]}
    route, text = command["route"], command["question"]
    if route == "bm25":
        body["query"] = {"bool": {"filter": filters, "must": [{"match": {"content_ltks": {
            "query": rag_tokenizer.tokenize(text), "operator": "or"}}}]}}
    elif route == "vector":
        vector, _ = current_model(identity).encode_queries(text)
        vector = vector.tolist()
        if not vector or not all(isinstance(v, (int, float)) and math.isfinite(v) for v in vector):
            raise ValueError("Query embedding is invalid")
        body["knn"] = {"field": f"q_{len(vector)}_vec", "query_vector": vector, "k": params["knn_top_k"],
                       "num_candidates": params["knn_num_candidates"], "similarity": params["similarity_threshold"],
                       "filter": {"bool": {"filter": filters}}}
    else:
        raise ValueError("Not a native baseline")
    response = SETTINGS.docStoreConn.es.options(max_retries=0, retry_on_timeout=False).search(index=index, body=body, request_timeout=60)
    if response.get("timed_out") or response.get("_shards", {}).get("failed", 0):
        raise ValueError("Elasticsearch search was incomplete")
    hits = [{"chunk_id": h["_id"], "document_id": h["_source"]["doc_id"], "score": h["_score"]}
            for h in response["hits"]["hits"]]
    return {"hits": hits, "backend_took_ms": response.get("took"), "returned_chunks": len(hits),
            "candidate_limit": params["knn_top_k"], "parameters": params,
            "request_without_vector_values": {key: value for key, value in body.items() if key != "knn"} if route == "bm25"
            else {"knn": {key: value for key, value in body["knn"].items() if key != "query_vector"}}}


def psr_query(command):
    identity = command["dataset_id"]
    dataset(identity)
    params = command["parameters"]
    window = params["rerank_candidates_count"]
    page_size = min(100, window)
    pages, hits, documents = [], [], set()
    for page in range(1, window // page_size + 1):
        payload = {**params, "question": command["question"], "dataset_ids": [identity], "page": page, "page_size": page_size}
        value = request("POST", "/retrieval", body=payload)
        pages.append({"request": payload, "response": value})
        if value.get("code") != 0:
            return {"hits": [], "pages": pages, "error_type": "PSRBusinessError", "error": str(value.get("message"))}
        rows = value["data"]["chunks"]
        for row in rows:
            hits.append({"chunk_id": row["id"], "document_id": row["document_id"], "score": row["similarity"]})
            documents.add(row["document_id"])
        if len(documents) >= 100 or len(rows) < page_size or params["rerank_id"]:
            break
    return {"hits": hits, "pages": pages, "returned_chunks": len(hits),
            "candidate_limit": window, "api_window_limit": min(100, window) if params["rerank_id"] else (window // page_size) * page_size}


def non_test_witness(command):
    identity = command["dataset_id"]
    question = "stem cell differentiation"
    reply = psr_query({"dataset_id": identity, "question": question, "parameters": command["parameters"]})
    if reply.get("error_type"):
        raise ValueError("Non-test PSR witness failed")
    vector, _ = current_model(identity).encode_queries(question)
    vector = vector.tolist()
    from api.db.db_models import DB, Knowledgebase
    from rag.nlp.search import index_name
    with DB.connection_context():
        index = index_name(Knowledgebase.get_by_id(identity).tenant_id)
    rows = reply["pages"][0]["response"]["data"]["chunks"]
    samples = []
    for hit in rows[:5]:
        stored = SETTINGS.docStoreConn.es.get(index=index, id=hit["id"], source_includes=["kb_id", "q_384_vec"])["_source"]
        if stored["kb_id"] != identity:
            raise ValueError("Witness chunk escaped the public dataset")
        samples.append({"chunk_id": hit["id"], "chunk_vector": stored["q_384_vec"],
                        "reported_vector_similarity": hit["vector_similarity"]})
    return {"question": question, "query_vector": vector, "samples": samples,
            "returned_scores": [{key: row[key] for key in ("id", "document_id", "term_similarity", "vector_similarity", "similarity")} for row in rows],
            "http_status": 200, "business_code": reply["pages"][0]["response"]["code"]}

def index_configuration(command):
    from api.db.db_models import DB, Knowledgebase
    from rag.nlp.search import index_name
    identity = command["dataset_id"]
    dataset(identity)
    with DB.connection_context():
        kb = Knowledgebase.get_by_id(identity)
    index = index_name(kb.tenant_id)
    mappings = SETTINGS.docStoreConn.es.indices.get_mapping(index=index)
    settings = SETTINGS.docStoreConn.es.indices.get_settings(index=index)
    properties = next(iter(mappings.values()))["mappings"]["properties"]
    field_config = {key: value for key, value in properties.items() if key == "content_ltks" or (key.startswith("q_") and key.endswith("_vec"))}
    index_settings = next(iter(settings.values()))["settings"]["index"]
    return {"index_name": index, "fields": field_config,
            "similarity": index_settings.get("similarity", {"default": {"type": "BM25", "k1": 1.2, "b": 0.75, "discount_overlaps": True}}),
            "analysis": index_settings.get("analysis", {}), "number_of_shards": index_settings.get("number_of_shards"),
            "backend_version": SETTINGS.docStoreConn.es.info()["version"]["number"],
            "index_total_chunks": SETTINGS.docStoreConn.es.count(index=index)["count"],
            "dataset_chunks": SETTINGS.docStoreConn.es.count(index=index, query={"term": {"kb_id": identity}})["count"]}

def dispatch(command):
    action = command["action"]
    if action == "status":
        return status()
    if action == "api":
        return api(command)
    if action == "upload":
        return upload(command)
    if action == "mapping":
        return mapping(command)
    if action == "defaults":
        return runtime_defaults()
    if action == "query":
        return psr_query(command) if command["route"] == "psr" else native_query(command)
    if action == "index_configuration":
        return index_configuration(command)
    if action == "model_sentinels":
        return model_sentinels(command)
    if action == "non_test_witness":
        return non_test_witness(command)
    raise ValueError("Unsupported worker action")


def safe_error(error):
    message = str(error).replace(TOKEN or "NEVER-A-TOKEN", "[REDACTED]")
    return re.sub(r"://[^/@\s]+:[^/@\s]+@", "://[REDACTED]@", message)[:600]


def main(client_class, worker_sha256):
    global WORKER_SHA256
    WORKER_SHA256 = worker_sha256
    try:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            initialize(client_class)
    except Exception as exc:  # noqa: BLE001 - The transport boundary must not expose credential-bearing errors.
        print(json.dumps({"ready": False, "error_type": type(exc).__name__}), flush=True)
        return
    print(json.dumps({"ready": True, "worker_sha256": WORKER_SHA256}), flush=True)
    for line in sys.stdin:
        started = time.perf_counter()
        try:
            command = json.loads(line)
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                result = dispatch(command)
            reply = {"ok": True, "data": result}
        except Exception as exc:  # noqa: BLE001 - Every upstream failure needs a redacted RPC result.
            reply = {"ok": False, "error_type": type(exc).__name__, "error": safe_error(exc)}
            reply["http_status"] = getattr(exc, "http_status", None)
        reply["worker_elapsed_s"] = time.perf_counter() - started
        print(json.dumps(reply, ensure_ascii=False, allow_nan=False), flush=True)


