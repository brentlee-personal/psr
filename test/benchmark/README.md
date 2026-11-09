# RAGFlow HTTP Benchmark CLI

## BRE-218 SciFact controlled evaluation

The separate `benchmark.beir` driver evaluates all 300 official test queries over 5,183 documents and 5,467 existing public chunks. It uses normal authorization in private PSR processes. It never registers accounts, changes tenant defaults, or replaces the business service.

The first campaign at `/Users/libin/repos/brentlee-personal/ragflow-psr/psr/evidence/bre-218-scifact-20261109` is preserved. Independent reconstruction confirmed its numbers, but found changing lexical scores and attempt persistence inside the timer. Its difference is an historical observation, not an isolated cosine-fix benefit.

The controlled campaign is `/Users/libin/repos/brentlee-personal/ragflow-psr/psr/evidence/bre-218-scifact-20261109-controlled`. Both private APIs use the same cached image, 4 GiB memory, 3 CPU limit, PYTHONHASHSEED=0, and the same deterministic WordNet ordering. The baseline retains the old ES adapter; the candidate recovers cosine. Neither publishes host ports. The existing public index and BGE-small TEI service are reused.

The accepted execution is bound to controller `dc9d500841c5ca15429711497084ea4be1532b73` and protocol SHA256 `20541a485d226f511f7f4537ba4c8bec8168d305362241cadf7e3ea85e096776`. Both runs completed 900 combinations with no failed or missing queries. Independent reconstruction passed within 1e-6; all 18,782 shared PSR hits had identical lexical scores, and all 300 candidate rankings were reproduced using only the cosine conversion. The private machine verdict SHA256 is `f6da0a2f8878d45c48a2de3b390de6570e750d5e66a9564c3ce9cf0740fb72e2`. This is English SciFact retrieval acceptance only.

The original preflight auditor SHA256 was `d032ae2e7386799500ebea73b57c3aa579369fab53a586091abbee9739ac1f0b`. The final auditor adds raw-vector geometry and complete post-run control checks, SHA256 `a51d8ccac615591970d7a10e7d6a502bc80bc5aced7b19ef66ceafc53673ae32`. Both originals are retained in the private evidence. The final delivery commit must not be described as the runtime-tested controller; restore the recorded controller to reproduce that exact execution, or obtain a new independent preflight for another source revision.

Start the owned APIs with the campaign's `start-controlled.py` from a clean execution checkout. The script requires the original verified ES adapter at `$KIROCREW_SCRATCH/baseline-es-conn.py`; restore its exact bytes from commit `cc8460a2cca95febd706e1a25f686abd52303dac` before starting. The source mounts must remain available throughout the run.

Use the session's Python 3.13 environment, with requests 2.32.5. The runtime images provide their own existing ES and ranx dependencies. The Docker socket is `unix:///Users/libin/.colima/default/docker.sock`. The driver is scoped to this recorded tenant and backend.

From the execution checkout:

```bash
env PYTHONPATH="$KIROCREW_SCRATCH/ragflow-psr-bre218/test" "$KIROCREW_SCRATCH/beir-venv/bin/python" -m benchmark.beir preflight --campaign-dir /Users/libin/repos/brentlee-personal/ragflow-psr/psr/evidence/bre-218-scifact-20261109-controlled
```

This collects both live bindings and freezes the protocol. It does not grant approval. An independent evaluator runs `beir_verify.py --preflight`, reads the real observations and committed code, and supplies a reviewed PASS. Automatic structural success remains PARTIAL. Formal queries refuse mismatched source, protocol, tool, manifest or evaluator identity.

After independent approval:

```bash
env PYTHONPATH="$KIROCREW_SCRATCH/ragflow-psr-bre218/test" "$KIROCREW_SCRATCH/beir-venv/bin/python" -u -m benchmark.beir run --campaign-dir /Users/libin/repos/brentlee-personal/ragflow-psr/psr/evidence/bre-218-scifact-20261109-controlled --run-id baseline
env PYTHONPATH="$KIROCREW_SCRATCH/ragflow-psr-bre218/test" "$KIROCREW_SCRATCH/beir-venv/bin/python" -u -m benchmark.beir run --campaign-dir /Users/libin/repos/brentlee-personal/ragflow-psr/psr/evidence/bre-218-scifact-20261109-controlled --run-id candidate
```

Each run executes three routes for all test queries. It checks live source, lexical assets, effective synonyms, WordNet, public vector values and non-test model sentinels before and after execution. Attempt-marker fsync completes before the query timer starts. The timer includes dispatch, encoding, HTTP or IPC, validation and document aggregation; result writes and scoring occur afterward.

Run names must begin with baseline or candidate. Finalized runs cannot be overwritten. A changed controller, protocol, index or resource binding needs a new frozen campaign and independent preflight. Unknown interrupted attempts are retained without retries or invented timing. Do not reindex completed public documents.

The independent standard-library verifier reconstructs ranks directly from raw responses and qrels. It checks every query, not only means. Preserve the original terminal records, attempt markers, per-query CSV, scores and failures. `report` reads completed artifacts without issuing retrieval requests.

The three routes share the same logical public dataset. BM25 statistics still include two non-candidate chunks in the tenant's physical index. PSR returns at most 64 chunks, so document Recall@100 uses the actual shorter list. Both limits remain explicit. Current document vectors include a 0.1 filename weight; TEI has a 512-token limit. Do not alter these settings based on test scores.

Use targeted benchmark tests and the ES/synonym unit checks. The independent verifier's `--self-test` covers metric boundaries, source overrides, empty differences and timing. Do not label an unreviewed or incomplete campaign accepted. This workflow evaluates English SciFact retrieval, not answer quality or production readiness.

Run (from repo root):
```
  PYTHONPATH=./test uv run -m benchmark [global flags] <chat|retrieval> [command flags]
  Global flags can be placed before or after the command.
```

If you run from another directory:
```
  PYTHONPATH=/Directory_name/ragflow/test uv run -m benchmark [global flags] <chat|retrieval> [command flags]
```

JSON args:
  For --dataset-payload, --chat-payload, --messages-json, --extra-body, --payload
  - Pass inline JSON: '{"key": "value"}'
  - Or use a file: '@/path/to/file.json'

Global flags
```
  --base-url
    Base server URL.
    Env: RAGFLOW_BASE_URL or HOST_ADDRESS
  --api-version
    API version string (default: v1).
    Env: RAGFLOW_API_VERSION
  --api-key
    API key for Authorization: Bearer <token>.
  --connect-timeout
    Connect timeout seconds (default: 5.0).
  --read-timeout
    Read timeout seconds (default: 60.0).
  --no-verify-ssl
    Disable SSL verification.
  --iterations
    Iterations per benchmark (default: 1).
  --concurrency
    Number of concurrent requests (default: 1). Uses multiprocessing.
  --json
    Output JSON report (plain stdout).
  --print-response
    Print response content per iteration (stdout). With --json, responses are included in the JSON output.
  --response-max-chars
    Truncate printed responses to N chars (0 = no limit).
```

Auth and bootstrap flags (used when --api-key is not provided)
```
  --login-email
    Login email.
    Env: RAGFLOW_EMAIL
  --login-nickname
    Nickname for registration. If omitted, defaults to email prefix when registering.
    Env: RAGFLOW_NICKNAME
  --login-password
    Login password (encrypted client-side). Requires pycryptodomex in the test group.
  --allow-register
    Attempt /users before login (best effort).
  --token-name
    Optional API token name for /system/new_token.
  --bootstrap-llm
    Ensure LLM factory API key is configured via /providers + /providers/{name}/instances.
  --llm-factory
    LLM factory name for bootstrap.
    Env: RAGFLOW_LLM_FACTORY
  --llm-api-key
    LLM API key for bootstrap.
    Env: ZHIPU_AI_API_KEY
  --llm-api-base
    Optional LLM API base URL.
    Env: RAGFLOW_LLM_API_BASE
  --set-tenant-info
    Set tenant defaults via /models/default.
  --tenant-llm-id
    Tenant chat model ID.
    Env: RAGFLOW_TENANT_LLM_ID
  --tenant-embd-id
    Tenant embedding model ID.
    Env: RAGFLOW_TENANT_EMBD_ID
  --tenant-img2txt-id
    Tenant image2text model ID.
    Env: RAGFLOW_TENANT_IMG2TXT_ID
  --tenant-asr-id
    Tenant ASR model ID (default empty).
    Env: RAGFLOW_TENANT_ASR_ID
  --tenant-tts-id
    Tenant TTS model ID.
    Env: RAGFLOW_TENANT_TTS_ID
```

Dataset/document flags (shared by chat and retrieval)
```
  --dataset-id
    Existing dataset ID.
  --dataset-ids
    Comma-separated dataset IDs.
  --dataset-name
    Dataset name when creating a new dataset.
    Env: RAGFLOW_DATASET_NAME
  --dataset-payload
    JSON body for dataset creation (see API docs).
  --document-path
    Document path to upload (repeatable).
  --document-paths-file
    File containing document paths, one per line.
  --parse-timeout
    Document parse timeout seconds (default: 120.0).
  --parse-interval
    Document parse poll interval seconds (default: 1.0).
  --teardown
    Delete created resources after run.
```

Chat command flags
```
  --chat-id
    Existing chat ID. If omitted, a chat is created.
  --chat-name
    Chat name when creating a new chat.
    Env: RAGFLOW_CHAT_NAME
  --chat-payload
    JSON body for chat creation (see API docs).
  --model
    Model field for OpenAI-compatible completion request.
    Env: RAGFLOW_CHAT_MODEL
  --message
    Single user message (required unless --messages-json is provided).
  --messages-json
    JSON list of OpenAI-format messages (required unless --message is provided).
  --extra-body
    JSON extra_body for OpenAI-compatible request.
```

Retrieval command flags
```
  --question
    Retrieval question (required unless provided in --payload).
  --payload
    JSON body for /api/v1/retrieval (see API docs).
  --document-ids
    Comma-separated document IDs for retrieval.
```

Model selection guidance
  - Embedding model is tied to the dataset.
    Set during dataset creation using --dataset-payload:
```
      {"name": "...", "embedding_model": "<model_name>@<provider>"}
```
    Or set tenant defaults via --set-tenant-info with --tenant-embd-id.
  - Chat model is tied to the chat assistant.
    Set during chat creation using --chat-payload:
```
      {"name": "...", "llm_id": "<model_name>@<provider>", "llm_setting": {}}
```
    Or set tenant defaults via --set-tenant-info with --tenant-llm-id.
  - --model is required by the OpenAI-compatible endpoint but does not override
    the chat assistant's configured model on the server.

What this CLI can do
  - This is a benchmark CLI. It always runs either a chat or retrieval benchmark
    and prints a report.
  - It can create datasets, upload documents, trigger parsing, and create chats
    as part of a benchmark run (setup for the benchmark).
  - It is not a general admin CLI; there are no standalone "create-only" or
    "manage" commands. Use the reports to capture created IDs for reuse.

## Interpreting failures and throughput

Each measured request contributes one success or failure, including when using
`--concurrency`. Connection failures, timeouts, non-2xx HTTP responses, invalid
response payloads, and API errors are recorded as failed samples so the remaining
iterations can run and produce a report. There are no automatic retries: a retry
would hide the original failure and change the measured workload. Authentication
and dataset/chat setup are outside the measured requests; setup failures still
stop the command.

A successful chat sample requires non-empty assistant content **and** a stream
completion marker (`[DONE]` or a non-empty `finish_reason`). Receiving some text
before an unexpected EOF is a failed, partial response. Partial text remains
available with `--print-response`, but failed samples are excluded from both
first-token and total-latency statistics. Completion measures protocol success,
not answer quality: a `length` finish reason can still indicate an answer limited
by the model's token budget. An empty retrieval result with `code: 0` is a
successful API request, not evidence of good retrieval quality.

Reports distinguish:

| JSON field | Definition |
| --- | --- |
| `qps` | All measured requests / measured wall-clock duration |
| `success_qps` | Successful requests / measured wall-clock duration |
| `failure_rate` | Failed requests / all measured requests, in the range 0–1 |

For example, if 10 measured requests finish in 5 seconds but only 6 succeed,
`qps` is 2, `success_qps` is 1.2, and `failure_rate` is 0.4. Comparing only QPS
can make a server that quickly rejects requests look faster than a healthy one.
The text report shows the same distinction and renders failure rate as a
percentage. If all requests fail, successful QPS is zero and successful latency
statistics are unavailable (`null` in JSON / `n/a` in text).

The command exits with status 1 if any measured request fails and 0 if all
measured requests succeed. Unexpected programming errors are not converted into
request failures.

### Testing the benchmark client

From the repository root, with the test dependencies installed:

```sh
uv run --group test pytest test/benchmark/tests
```

These tests use response fixtures and a loopback HTTP server. They also invoke
the actual CLI with sequential and multiprocessing workloads. No RAGFlow server,
database, model, or external credentials are required.

Do I need the dataset ID?
  - If the CLI creates a dataset, it uses the returned dataset ID internally.
    You do not need to supply it for that same run.
  - The report prints "Created Dataset ID" so you can reuse it later with
    --dataset-id or --dataset-ids.
  - Dataset name is only used at creation time. Selection is always by ID.

Examples

Example: chat benchmark creating dataset + upload + parse + chat (login + register)
```
  PYTHONPATH=./test uv run -m benchmark chat \
    --base-url http://127.0.0.1:9380 \
    --allow-register \
    --login-email "qa@infiniflow.org" \
    --login-password "123" \
    --bootstrap-llm \
    --llm-factory ZHIPU-AI \
    --llm-api-key $ZHIPU_AI_API_KEY \
    --dataset-name "bench_dataset" \
    --dataset-payload '{"name":"bench_dataset","embedding_model":"BAAI/bge-small-en-v1.5@Builtin"}' \
    --document-path test/benchmark/test_docs/Doc1.pdf \
    --document-path test/benchmark/test_docs/Doc2.pdf \
    --document-path test/benchmark/test_docs/Doc3.pdf \
    --chat-name "bench_chat" \
    --chat-payload '{"name":"bench_chat","llm_id":"glm-4-flash@ZHIPU-AI","llm_setting":{}}' \
    --message "What is the purpose of RAGFlow?" \
    --model "glm-4-flash@ZHIPU-AI"
```

Example: chat benchmark with existing dataset + chat id (no creation)
```
  PYTHONPATH=./test uv run -m benchmark chat \
    --base-url http://127.0.0.1:9380 \
    --chat-id <existing_chat_id> \
    --login-email "qa@infiniflow.org" \
    --login-password "123" \
    --message "What is the purpose of RAGFlow?" \
    --model "glm-4-flash@ZHIPU-AI"
```

Example: retrieval benchmark creating dataset + upload + parse
```
  PYTHONPATH=./test uv run -m benchmark retrieval \
    --base-url http://127.0.0.1:9380 \
    --allow-register \
    --login-email "qa@infiniflow.org" \
    --login-password "123" \
    --bootstrap-llm \
    --llm-factory ZHIPU-AI \
    --llm-api-key $ZHIPU_AI_API_KEY \
    --dataset-name "bench_dataset" \
    --dataset-payload '{"name":"bench_dataset","embedding_model":"BAAI/bge-small-en-v1.5@Builtin"}' \
    --document-path test/benchmark/test_docs/Doc1.pdf \
    --document-path test/benchmark/test_docs/Doc2.pdf \
    --document-path test/benchmark/test_docs/Doc3.pdf \
    --question "What does RAG mean?"
```

Example: retrieval benchmark with existing dataset IDs
```
  PYTHONPATH=./test uv run -m benchmark retrieval \
    --base-url http://127.0.0.1:9380 \
    --login-email "qa@infiniflow.org" \
    --login-password "123" \
    --dataset-ids "<dataset_id_1>,<dataset_id_2>" \
    --question "What does RAG mean?"
```

Example: retrieval benchmark with existing dataset IDs and document IDs
```
  PYTHONPATH=./test uv run -m benchmark retrieval \
    --base-url http://127.0.0.1:9380 \
    --login-email "qa@infiniflow.org" \
    --login-password "123" \
    --dataset-id "<dataset_id>" \
    --document-ids "<doc_id_1>,<doc_id_2>" \
    --question "What does RAG mean?"
```

Quick scripts

These scripts create a dataset,
upload/parse docs from test/benchmark/test_docs, run the benchmark, and clean up.
The both script runs retrieval then chat on the same dataset, then deletes it.

- Make sure to run ```uv sync --python 3.13 --group test ``` before running the commands.
- It is also necessary to run these commands prior to initializing your containers if you plan on using the built-in embedded model: ```echo -e "TEI_MODEL=BAAI/bge-small-en-v1.5" >> docker/.env```
  and ```echo -e "COMPOSE_PROFILES=\${COMPOSE_PROFILES},tei-cpu" >> docker/.env```

Chat only:
```
  ./test/benchmark/run_chat.sh
```

Retrieval only:
```
  ./test/benchmark/run_retrieval.sh
```

Both (retrieval then chat on the same dataset):
```
  ./test/benchmark/run_retrieval_chat.sh
```

Requires:
  - ZHIPU_AI_API_KEY exported in your shell.

Defaults used:
  - Base URL: http://127.0.0.1:9380
  - Login: qa@infiniflow.org / 123 (with allow-register)
  - LLM bootstrap: ZHIPU-AI with $ZHIPU_AI_API_KEY
  - Dataset: bench_dataset (BAAI/bge-small-en-v1.5@Builtin)
  - Chat: bench_chat (glm-4-flash@ZHIPU-AI)
  - Chat message: "What is the purpose of RAGFlow?"
  - Retrieval question: "What does RAG mean?"
  - Iterations: 1
  - concurrency:f 4
