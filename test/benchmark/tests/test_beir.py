import json
import os
import tempfile
import unittest
import zipfile
from pathlib import Path

from benchmark.beir import load_data, unique_records


class DataTests(unittest.TestCase):
    def test_duplicate_ids_are_not_silently_overwritten(self):
        raw = b'{"_id":"same","text":"one"}\n{"_id":"same","text":"two"}\n'
        with self.assertRaisesRegex(ValueError, "duplicate ID"):
            unique_records(raw, "corpus")

    def archive(self, directory, dangling=False):
        path = Path(directory) / "fixture.zip"
        corpus = "\n".join(json.dumps({"_id": str(i), "title": "Title", "text": "Abstract"}) for i in range(5183))
        queries = "\n".join(json.dumps({"_id": str(i), "text": "Query"}) for i in range(301))
        labels = "query-id\tcorpus-id\tscore\n" + "\n".join(f"{i}\t{i if not dangling else 99999}\t1" for i in range(300))
        with zipfile.ZipFile(path, "w") as archive:
            archive.writestr("scifact/corpus.jsonl", corpus)
            archive.writestr("scifact/queries.jsonl", queries)
            archive.writestr("scifact/qrels/test.tsv", labels)
        return path

    def test_test_ids_come_from_test_labels(self):
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory:
            _, corpus, queries, qrels = load_data(self.archive(directory))
        self.assertEqual((len(corpus), len(queries), len(qrels)), (5183, 301, 300))
        self.assertNotIn("300", qrels)

    def test_dangling_relevant_document_is_rejected(self):
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory, self.assertRaisesRegex(ValueError, "dangling qrel"):
            load_data(self.archive(directory, dangling=True))

    def test_prepare_preserves_the_frozen_manifest_bytes(self):
        import hashlib
        from unittest.mock import patch

        from benchmark.beir import prepare_data, write_json
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory:
            archive = self.archive(directory)
            campaign = Path(directory) / "campaign"
            with patch("benchmark.beir.OFFICIAL_MD5", hashlib.md5(archive.read_bytes()).hexdigest()):
                manifest = prepare_data(campaign, archive)
                manifest.pop("acquired_for_campaign_at")
                manifest.pop("source_cache_mtime")
                path = campaign / "data-manifest.json"
                write_json(path, manifest)
                before = path.read_bytes()
                self.assertEqual(prepare_data(campaign, archive), manifest)
                self.assertEqual(path.read_bytes(), before)


class ParseWaitTests(unittest.TestCase):
    def client(self, pages):
        class Client:
            def request_json(self, method, path, params):
                return {"code": 0, "data": pages.get(params["page"], {"total": 101, "docs": []})}
        return Client()

    def pages(self, final_state):
        return {1: {"total": 101, "docs": [{"id": str(i), "run": "DONE"} for i in range(100)]},
                2: {"total": 101, "docs": [{"id": "100", "run": final_state}]}}

    def test_later_running_page_does_not_pass(self):
        from benchmark.dataset import DatasetError, wait_for_parse_done
        with self.assertRaises(DatasetError):
            wait_for_parse_done(self.client(self.pages("RUNNING")), "evaluation", ["0", "100"], 0, 0)

    def test_empty_listing_does_not_pass_missing_target(self):
        from benchmark.dataset import DatasetError, wait_for_parse_done
        with self.assertRaises(DatasetError):
            wait_for_parse_done(self.client({}), "evaluation", ["missing"], 0, 0)

    def test_all_target_pages_done_pass(self):
        from benchmark.dataset import wait_for_parse_done
        wait_for_parse_done(self.client(self.pages("DONE")), "evaluation", ["0", "100"], 0, 0)

class RankingTests(unittest.TestCase):
    def test_first_hit_deduplicates_docs_without_resorting_ties(self):
        from benchmark.beir_metrics import aggregate_documents
        result = aggregate_documents([{"chunk_id": "a", "document_id": "d1", "score": 0.5},
                                      {"chunk_id": "b", "document_id": "d1", "score": 0.9},
                                      {"chunk_id": "c", "document_id": "d2", "score": 0.5}], {"d1": "same", "d2": "other"})
        self.assertEqual([d["document_id"] for d in result], ["same", "other"])
        self.assertEqual(result[0]["score"], 0.5)

    def test_repeated_page_chunk_is_rejected(self):
        from benchmark.beir_metrics import aggregate_documents
        hit = {"chunk_id": "a", "document_id": "d1", "score": 0.5}
        with self.assertRaisesRegex(ValueError, "Duplicate chunk"):
            aggregate_documents([hit, hit], {"d1": "original"})

    def test_unknown_or_nonfinite_result_is_rejected(self):
        from benchmark.beir_metrics import aggregate_documents
        for mapping, score in [({}, 1.0), ({"d1": "original"}, float("nan"))]:
            with self.assertRaises(ValueError):
                aggregate_documents([{"chunk_id": "a", "document_id": "d1", "score": score}], mapping)

class RecoveryTests(unittest.TestCase):
    def test_formal_run_refuses_an_unapproved_protocol_before_worker_start(self):
        from unittest.mock import patch

        from benchmark import beir
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory:
            root = Path(directory)
            for verdict, code, evaluator in [("PARTIAL", "fixed", "/root/independent_eval"), ("PASS", "other", "/root/independent_eval"), ("PASS", "fixed", "/root")]:
                beir.write_json(root / "preflight-verdict.json", {"verdict": verdict, "controller_code_sha": code, "evaluator_id": evaluator,
                                "approved_at": "2000-01-01T00:00:00+00:00"})
                with patch.object(beir, "source_identity", return_value={"code_sha": "fixed"}), patch.object(beir, "Worker") as worker, \
                     self.assertRaisesRegex(ValueError, "independent preflight"):
                    beir.run_evaluation(root, "baseline")
                worker.assert_not_called()

    def test_attempt_persistence_is_outside_query_timing(self):
        from unittest.mock import MagicMock, patch

        from benchmark import beir
        clock, captured = [0.0], []
        worker = MagicMock()
        worker.container = "ragflow-psr-test"
        worker.__enter__.return_value = worker
        def call(action, **arguments):
            if action == "query":
                clock[0] += 3.0
                return {"hits": [{"chunk_id": "chunk", "document_id": "doc", "score": 1.0}]}
            return {"source_hashes": {}, "worker_sha256": "fixed", "nlp": {}}
        worker.call.side_effect = call
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory:
            root = Path(directory)
            protocol = {"execution": {"warmup": []}, "routes": {"psr": {"parameters": {}}}}
            for name, value in {"resources.json": {"dataset_id": "dataset"}, "data/test-queries.json": [{"query_id": "query", "text": "fixture"}],
                                "document-map.json": [{"document_id": "doc", "scifact_id": "original"}], "chunk-map.json": {}, "protocol.json": protocol}.items():
                beir.write_json(root / name, value)
            beir.write_json(root / "preflight-verdict.json", {"verdict": "PASS", "controller_code_sha": "fixed",
                            "protocol_sha256": beir.sha256((root / "protocol.json").read_bytes()), "evaluator_id": "/root/independent_eval"})
            observation = {"container": {"name": worker.container}, "runtime_sources": {}, "vector_values_sha256": "fixed", "model_sentinels": {}}
            beir.write_json(root / "preflight.json", {"variants": {"baseline": observation}})
            beir.write_json(root / "preflight-verdict.json", {"verdict": "PASS", "controller_code_sha": "fixed",
                            "protocol_sha256": beir.sha256((root / "protocol.json").read_bytes()), "evaluator_id": "/root/independent_eval",
                            "approved_at": "2000-01-01T00:00:00+00:00", "input_manifest_sha256": beir.sha256((root / "preflight.json").read_bytes()),
                            "tool_sha256": beir.sha256(Path(beir.__file__).with_name("beir_verify.py").read_bytes())})
            real_write = beir.write_json
            def slow_write(*args):
                clock[0] += 10.0
                real_write(*args)
            with patch.object(beir, "Worker", return_value=worker), patch.object(beir, "source_identity", return_value={"code_sha": "fixed"}), \
                 patch.object(beir, "freeze_protocol", return_value=protocol), patch.object(beir, "write_json", side_effect=slow_write), \
                 patch.object(beir, "observe_runtime", return_value=observation), \
                 patch.object(beir, "save_scoring", side_effect=lambda campaign, target, rows: captured.extend(rows)), \
                 patch("time.perf_counter", side_effect=lambda: clock[0]):
                beir.run_evaluation(root, "baseline")
        self.assertEqual(len(captured), 3)
        self.assertEqual([row["elapsed_s"] for row in captured], [3.0, 3.0, 3.0])

    def test_interrupted_attempt_is_retained_without_retry_or_fake_time(self):
        from benchmark.beir import existing_result, write_json
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory:
            path = Path(directory) / "result.json"
            marker = {"run_id": "baseline", "route": "psr", "query_id": "query", "protocol_sha256": "fixed",
                      "state": "in_flight", "attempt": 1, "documents": []}
            write_json(path.with_suffix(".attempt.json"), marker)
            result = existing_result(path, "baseline", "psr", "query", "fixed")
            self.assertEqual(result["attempt_state"], "unknown")
            self.assertEqual(result["state"], "failed")
            self.assertIsNone(result["elapsed_s"])
            self.assertTrue(path.with_suffix(".attempt.json").exists())

    def test_resume_rejects_a_result_from_another_route(self):
        from benchmark.beir import existing_result, write_json
        with tempfile.TemporaryDirectory(dir=os.environ["KIROCREW_SCRATCH"]) as directory:
            path = Path(directory) / "result.json"
            write_json(path, {"run_id": "baseline", "route": "vector", "query_id": "query", "protocol_sha256": "fixed"})
            with self.assertRaisesRegex(ValueError, "identity"):
                existing_result(path, "baseline", "psr", "query", "fixed")

class CredentialBoundaryTests(unittest.TestCase):
    def test_invalid_container_is_rejected_before_execution(self):
        from unittest.mock import patch

        from benchmark.beir import Worker
        with patch.dict(os.environ, {"PSR_BEIR_CONTAINER": "other-service"}), self.assertRaisesRegex(ValueError, "container"):
            Worker()

    def test_rpc_error_redacts_the_token_and_url_password(self):
        from unittest.mock import patch

        from benchmark import beir_engine
        with patch.object(beir_engine, "TOKEN", "fixture-token"):
            result = beir_engine.safe_error(ValueError("fixture-token https://user:secret@example.test/"))
        self.assertNotIn("fixture-token", result)
        self.assertNotIn("secret", result)
        self.assertIn("[REDACTED]", result)

if __name__ == "__main__":
    unittest.main()
