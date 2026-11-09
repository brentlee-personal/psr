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
